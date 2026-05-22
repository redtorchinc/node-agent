package timesync

import (
	"context"
	"encoding/binary"
	"math"
	"net"
	"sync"
	"testing"
	"time"
)

// TestNTPTimestampRoundtrip checks that toNTPTimestamp ∘ fromNTPTimestamp
// is the identity to within sub-ns precision. NTP's 32-bit fractional
// field gives ~233 ps of resolution — way finer than Go's time.Time can
// distinguish — so the roundtrip should be exact at ns scale.
func TestNTPTimestampRoundtrip(t *testing.T) {
	cases := []time.Time{
		time.Unix(0, 0).UTC(),
		time.Unix(1748000000, 123456789).UTC(),
		time.Unix(2000000000, 999999999).UTC(),
	}
	for _, want := range cases {
		got := fromNTPTimestamp(toNTPTimestamp(want)).UTC()
		// Allow 1ns slop for integer-truncation in the fractional conversion.
		if d := got.Sub(want); d < -time.Nanosecond || d > time.Nanosecond {
			t.Errorf("roundtrip %v: got %v (delta %v)", want, got, d)
		}
	}
}

// TestQueryNTP_LocalServer spins up a tiny in-process UDP NTP server
// that returns a known offset, then verifies queryNTP computes
// offset/RTT correctly.
//
// We can't validate against an external NTP server in unit tests (no
// network, slow, flaky). The in-process server is enough to exercise
// the parsing path end-to-end.
func TestQueryNTP_LocalServer(t *testing.T) {
	srv := startFakeNTPServer(t)
	defer srv.Close()

	// Inject a known offset: pretend the server is 50ms ahead of us.
	srv.SetOffset(50 * time.Millisecond)
	srv.SetStratum(2)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := queryNTP(ctx, srv.Addr())
	if err != nil {
		t.Fatalf("queryNTP: %v", err)
	}
	if resp.stratum != 2 {
		t.Errorf("stratum = %d, want 2", resp.stratum)
	}
	// queryNTP returns offset = server - local (the standard NTP sign).
	// We told the fake server to claim it's 50ms ahead, so offset should
	// be ~+50ms with some local clock noise.
	gotMS := float64(resp.offset) / float64(time.Millisecond)
	if math.Abs(gotMS-50) > 10 {
		t.Errorf("offset = %.2f ms, want ~50 ms (±10)", gotMS)
	}
	// RTT should be small (loopback) and non-negative.
	if resp.rtt < 0 || resp.rtt > 100*time.Millisecond {
		t.Errorf("rtt = %v, want small positive", resp.rtt)
	}
}

// TestQueryNTP_Timeout verifies the path where the server never replies.
// We use a UDP "discard" sink that drops packets silently.
func TestQueryNTP_Timeout(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if _, err := queryNTP(ctx, pc.LocalAddr().String()); err == nil {
		t.Fatal("queryNTP returned nil error on timeout, want non-nil")
	}
}

// TestServerProbeSnapshot_BeforeFirstProbe verifies the snapshot before
// any refresh has run: OffsetMS/RTTMS/Stratum are nil, but Host and
// ProbeIntervalS are populated.
func TestServerProbeSnapshot_BeforeFirstProbe(t *testing.T) {
	p := NewServerProbe("time.example.invalid")
	s := p.Snapshot()
	if s == nil {
		t.Fatal("Snapshot returned nil")
	}
	if s.Host != "time.example.invalid" {
		t.Errorf("Host = %q, want time.example.invalid", s.Host)
	}
	if s.ProbeIntervalS != int64(ServerProbeInterval/time.Second) {
		t.Errorf("ProbeIntervalS = %d, want %d", s.ProbeIntervalS, int64(ServerProbeInterval/time.Second))
	}
	if s.OffsetMS != nil || s.RTTMS != nil || s.Stratum != nil {
		t.Errorf("expected nil OffsetMS/RTTMS/Stratum before first probe, got %+v", s)
	}
	if s.LastProbeAgeS != nil {
		t.Errorf("LastProbeAgeS = %v, want nil before first probe", s.LastProbeAgeS)
	}
}

// TestServerProbeRefresh_RecordsSuccess exercises a successful refresh
// end-to-end and checks the wire-shape conversion: queryNTP returns
// server-local offset, ServerInfo exposes local-server (sign flipped).
func TestServerProbeRefresh_RecordsSuccess(t *testing.T) {
	srv := startFakeNTPServer(t)
	defer srv.Close()
	srv.SetOffset(75 * time.Millisecond) // server reports itself 75ms ahead
	srv.SetStratum(3)

	p := NewServerProbe(srv.Addr())
	p.refresh(context.Background())

	s := p.Snapshot()
	if s.OffsetMS == nil {
		t.Fatal("OffsetMS still nil after successful refresh")
	}
	// Wire convention: local - server, positive = local ahead.
	// Server claims 75ms ahead → local is 75ms behind → wire value ≈ -75.
	if math.Abs(*s.OffsetMS-(-75)) > 10 {
		t.Errorf("OffsetMS = %.2f, want ~-75 (±10)", *s.OffsetMS)
	}
	if s.Stratum == nil || *s.Stratum != 3 {
		t.Errorf("Stratum = %v, want 3", s.Stratum)
	}
	if s.Error != "" {
		t.Errorf("Error = %q, want empty on success", s.Error)
	}
	if s.LastProbeAgeS == nil {
		t.Error("LastProbeAgeS still nil after refresh")
	}
}

// TestServerProbe_StartHonoursEmptyHost verifies the opt-out path: an
// empty host must not spawn a goroutine or panic.
func TestServerProbe_StartHonoursEmptyHost(t *testing.T) {
	p := NewServerProbe("")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Start(ctx) // must return immediately without blocking or panicking
}

// --- in-process fake NTP server ---

// fakeNTPServer is a minimal UDP server that answers a single client
// request with a synthesized response. Adjustable offset and stratum
// let tests drive specific cases.
type fakeNTPServer struct {
	conn    net.PacketConn
	mu      sync.Mutex
	offset  time.Duration
	stratum int
	done    chan struct{}
}

func startFakeNTPServer(t *testing.T) *fakeNTPServer {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeNTPServer{conn: pc, stratum: 2, done: make(chan struct{})}
	go s.serve()
	return s
}

func (s *fakeNTPServer) Addr() string {
	return s.conn.LocalAddr().String()
}

func (s *fakeNTPServer) SetOffset(o time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offset = o
}

func (s *fakeNTPServer) SetStratum(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stratum = n
}

func (s *fakeNTPServer) Close() {
	close(s.done)
	_ = s.conn.Close()
}

func (s *fakeNTPServer) serve() {
	buf := make([]byte, 48)
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := s.conn.ReadFrom(buf)
		select {
		case <-s.done:
			return
		default:
		}
		if err != nil {
			continue
		}
		if n < 48 {
			continue
		}

		s.mu.Lock()
		offset := s.offset
		stratum := s.stratum
		s.mu.Unlock()

		// Build response: copy client's TransmitTimestamp into our
		// OriginTimestamp field, then set Receive/Transmit timestamps
		// at (now + offset) to fake a clock that is `offset` ahead of
		// real time.
		var resp [48]byte
		resp[0] = 0x24 // LI=0, VN=4, Mode=4 (server)
		resp[1] = byte(stratum)
		// OriginTimestamp = the client's TransmitTimestamp (echo back).
		copy(resp[24:32], buf[40:48])
		t := time.Now().Add(offset)
		ts := toNTPTimestamp(t)
		binary.BigEndian.PutUint64(resp[32:40], ts) // ReceiveTimestamp
		binary.BigEndian.PutUint64(resp[40:48], ts) // TransmitTimestamp

		_, _ = s.conn.WriteTo(resp[:], addr)
	}
}
