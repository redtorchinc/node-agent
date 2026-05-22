package timesync

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// ServerProbeInterval is how often the background goroutine queries the
// configured NTP server. 60s is a deliberate trade-off: short enough to
// catch a real clock drift inside one /health poll window, long enough
// that we don't look like an NTP client abuser to operators of public
// time servers (Cloudflare, NIST, pool.ntp.org all set their published
// query rate guidance around once-per-minute for client devices).
const ServerProbeInterval = 60 * time.Second

// ntpQueryTimeout caps the per-attempt UDP wait. 3 s is generous for
// anycast NTP (typical RTT < 50 ms) but tolerant of one packet loss
// retry inside Go's net.Read window.
const ntpQueryTimeout = 3 * time.Second

// ntpEpochOffset is the difference between the NTP epoch (1900-01-01)
// and the Unix epoch (1970-01-01), in seconds. Hard-coded because it
// never changes (RFC 5905 §6 Reference Implementation).
const ntpEpochOffset = 2208988800

// ntpPacketSize is the fixed-length NTP v4 packet (RFC 5905 §7.3). We
// don't send/receive any of the optional authenticator/extension
// trailers — they're unused outside NTS deployments.
const ntpPacketSize = 48

// ServerProbe queries an NTP server on a background loop and caches
// the latest result for the /health composer to read non-blockingly.
//
// The probe is intentionally minimal: stdlib UDP, no third-party NTP
// library. The protocol is fixed at 48 bytes (RFC 5905) and the
// offset/RTT math is the standard four-timestamp formula. Adding a
// dependency for ~80 lines of well-specified protocol code would be
// out of step with the rest of the agent's minimal-dependency posture.
type ServerProbe struct {
	host string

	mu     sync.RWMutex
	snap   serverSnapshot
	primed bool
}

// serverSnapshot is the internal cache. ServerInfo is built from this
// at /health time by Snapshot(). Splitting the storage from the wire
// shape lets us record an absolute LastTS internally (cheaper to
// compare against than recomputing an age every poll).
type serverSnapshot struct {
	offsetMS *float64
	rttMS    *float64
	stratum  *int
	lastTS   time.Time
	err      string
}

// NewServerProbe returns a probe targeting host. The host may include
// a ":port" suffix; "123" is assumed when absent. Empty host is
// invalid — the caller should not construct a ServerProbe in that case
// (the reporter respects the empty-string config opt-out before
// reaching here).
func NewServerProbe(host string) *ServerProbe {
	return &ServerProbe{host: host}
}

// Host returns the configured server host (as supplied to NewServerProbe).
func (s *ServerProbe) Host() string {
	if s == nil {
		return ""
	}
	return s.host
}

// Snapshot returns the latest cached result as a wire-ready ServerInfo.
// Safe for concurrent use. Always returns a non-nil pointer; check
// OffsetMS for nil to distinguish "no successful probe yet" from "have
// a reading".
func (s *ServerProbe) Snapshot() *ServerInfo {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	info := &ServerInfo{
		Host:           s.host,
		ProbeIntervalS: int64(ServerProbeInterval / time.Second),
	}
	if s.primed {
		info.OffsetMS = s.snap.offsetMS
		info.RTTMS = s.snap.rttMS
		info.Stratum = s.snap.stratum
		info.Error = s.snap.err
		age := int(time.Since(s.snap.lastTS).Seconds())
		if age < 0 {
			age = 0
		}
		info.LastProbeAgeS = &age
	}
	return info
}

// Start launches the background refresh loop. The first refresh runs
// inline so /health calls within the first 60s of agent boot see real
// data rather than a nil OffsetMS. Stops on ctx cancel.
func (s *ServerProbe) Start(ctx context.Context) {
	if s == nil || s.host == "" {
		return
	}
	s.refresh(ctx)
	go func() {
		t := time.NewTicker(ServerProbeInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.refresh(ctx)
			}
		}
	}()
}

// refresh runs one NTP query and stores the result. On error the
// snapshot retains its previous OffsetMS/RTTMS/Stratum (so a brief
// outage doesn't drop the last-known value to nil) but Error is set
// to the new failure. On success, Error clears.
func (s *ServerProbe) refresh(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, ntpQueryTimeout)
	defer cancel()
	resp, err := queryNTP(ctx, s.host)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.primed = true
	s.snap.lastTS = time.Now()
	if err != nil {
		s.snap.err = err.Error()
		return
	}
	// queryNTP returns offset = server - local (the standard NTP sign).
	// /health exposes local - server for "is local ahead" intuition.
	offsetMS := -float64(resp.offset) / float64(time.Millisecond)
	rttMS := float64(resp.rtt) / float64(time.Millisecond)
	stratum := resp.stratum
	s.snap.offsetMS = &offsetMS
	s.snap.rttMS = &rttMS
	s.snap.stratum = &stratum
	s.snap.err = ""
}

// ntpResponse holds parsed values from a successful NTP exchange. All
// internal — the wire shape is ServerInfo.
type ntpResponse struct {
	stratum int
	offset  time.Duration
	rtt     time.Duration
}

// queryNTP performs one NTP v4 client query and computes
// offset / RTT using the standard four-timestamp formula. Exported
// for tests in the same package; not part of the public API.
//
// Sign convention: offset is the amount to ADD to the local clock to
// match the server (i.e. positive offset = local is behind server).
// The caller is responsible for re-expressing this to match the wire
// contract.
func queryNTP(ctx context.Context, host string) (*ntpResponse, error) {
	if host == "" {
		return nil, errors.New("empty host")
	}
	addr := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		// No explicit port — default to 123. SplitHostPort fails on
		// "time.cloudflare.com" but succeeds on "time.cloudflare.com:123".
		addr = net.JoinHostPort(host, "123")
	}

	d := net.Dialer{Timeout: ntpQueryTimeout}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}
	defer conn.Close()

	deadline := time.Now().Add(ntpQueryTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	// Build minimal client request: LI=0, VN=4, Mode=3 (client).
	// All other fields zero except TransmitTimestamp (t1), which the
	// server echoes back as OriginTimestamp so we can correlate
	// request/response.
	var req [ntpPacketSize]byte
	req[0] = 0x23 // 0b00_100_011 = LI 0, VN 4, Mode 3

	t1 := time.Now()
	binary.BigEndian.PutUint64(req[40:], toNTPTimestamp(t1))

	if _, err := conn.Write(req[:]); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	var resp [ntpPacketSize]byte
	if _, err := conn.Read(resp[:]); err != nil {
		// Common case: timeout. Surface as a clean error string so
		// /health.time_sync.server.error reads sensibly.
		if strings.Contains(err.Error(), "i/o timeout") {
			return nil, errors.New("timeout")
		}
		return nil, fmt.Errorf("recv: %w", err)
	}
	t4 := time.Now()

	stratum := int(resp[1])
	if stratum == 0 {
		// Stratum 0 with a 4-byte ASCII Reference ID is a Kiss-of-Death
		// (RFC 5905 §7.4). The most common one — RATE — means the server
		// is asking us to back off. Treat as error; we wait ServerProbeInterval
		// before retrying, which already exceeds the recommended floor.
		refID := string(resp[12:16])
		refID = strings.TrimRight(refID, "\x00")
		return nil, fmt.Errorf("kiss-of-death: %s", refID)
	}

	t2 := fromNTPTimestamp(binary.BigEndian.Uint64(resp[32:40])) // ReceiveTimestamp
	t3 := fromNTPTimestamp(binary.BigEndian.Uint64(resp[40:48])) // TransmitTimestamp

	// Standard NTP offset/delay formula (RFC 5905 §8). Offset is
	// server-local: "how far to move local to match server."
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	rtt := t4.Sub(t1) - t3.Sub(t2)
	if rtt < 0 {
		rtt = 0
	}

	return &ntpResponse{stratum: stratum, offset: offset, rtt: rtt}, nil
}

// toNTPTimestamp converts a Go time.Time to the 64-bit NTP timestamp
// format (32 bits seconds since NTP epoch | 32 bits fractional seconds).
func toNTPTimestamp(t time.Time) uint64 {
	sec := uint64(t.Unix() + ntpEpochOffset)
	frac := uint64(t.Nanosecond()) * (1 << 32) / 1_000_000_000
	return (sec << 32) | (frac & 0xFFFFFFFF)
}

// fromNTPTimestamp is the inverse of toNTPTimestamp.
func fromNTPTimestamp(v uint64) time.Time {
	sec := int64(v>>32) - ntpEpochOffset
	frac := int64(v & 0xFFFFFFFF)
	nsec := frac * 1_000_000_000 / (1 << 32)
	return time.Unix(sec, nsec)
}
