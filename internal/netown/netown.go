// Package netown maps live and recently-closed sockets to the local
// process, user, and service unit that owns them, so the gateway can
// join NetFlow/IPFIX observations to host-side ownership.
//
// Wire contract: docs/api/network-flows.md (summarized in SPEC.md
// §Network flow ownership). The three read surfaces are:
//
//	GET /network/sockets — current sockets with owner metadata
//	GET /network/flows   — rolling window of live + recently-closed sockets
//	GET /network/resolve — one gateway 5-tuple → best local owner
//
// Design constraints (from the v0.3.0 review of issue #21):
//
//   - Ownership only. No packet payloads, no byte/packet counters in this
//     slice — the gateway already has flow volumes from NetFlow; per-socket
//     deltas would need netlink inet_diag (a new dependency) and are
//     deferred. The wire shapes reserve the fields as optional.
//   - Command lines are redacted (secret-shaped args) before truncation,
//     then capped at Config.CmdlineMaxBytes. See redact.go.
//   - The agent cannot attribute a socket to a case-manager workflow; the
//     only local identity is the training-mode run_id, surfaced at the
//     response envelope level (training_run_id) for temporal joins in the
//     backend. There is deliberately no per-item workflow field.
//
// Concurrency: one RWMutex guards the entry map. The background sampler
// (Run) takes the write lock briefly per poll; read endpoints take RLock.
package netown

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config tunes the collector. Zero values are replaced with defaults in New.
type Config struct {
	PollIntervalS   int // background sample cadence; default 10
	WindowS         int // retention for closed sockets; default 300
	CmdlineMaxBytes int // cmdline_head cap after redaction; default 240
}

// RawConn is one OS-observed socket, normalized by a Sampler. Neutral
// struct so tests can feed synthetic samples without gopsutil types.
type RawConn struct {
	Proto      string // "tcp" | "udp"
	LocalAddr  string
	LocalPort  uint32
	RemoteAddr string
	RemotePort uint32
	State      string // lowercase ("established", "listen", "" for udp)
	PID        int32  // 0 = unknown owner
}

// Sampler produces the current socket table. Implemented by
// gopsutilSampler; tests substitute fakes via NewWithDeps.
type Sampler interface {
	Sample() ([]RawConn, error)
	// Source names the underlying data path for the wire `source` field
	// ("procfs", "lsof", "iphlpapi", ...).
	Source() string
}

// ProcInfo is per-PID enrichment. CmdlineRaw is redacted + truncated by
// the collector before it reaches the wire.
type ProcInfo struct {
	Name          string
	Exe           string
	User          string
	UID           *int32
	CmdlineRaw    []string
	Cgroup        string // Linux only
	Service       string // systemd unit parsed from cgroup, Linux only
	ContainerID   string
	ContainerName string
}

// ProcResolver enriches a PID. Implemented by gopsutilProcs; tests
// substitute fakes.
type ProcResolver interface {
	Info(pid int32) (ProcInfo, error)
}

// SocketItem is the wire shape for one owned socket, shared by /sockets
// and (embedded in FlowItem) /flows.
type SocketItem struct {
	Proto         string `json:"proto"`
	State         string `json:"state,omitempty"`
	LocalAddr     string `json:"local_addr"`
	LocalPort     uint32 `json:"local_port"`
	RemoteAddr    string `json:"remote_addr,omitempty"`
	RemotePort    uint32 `json:"remote_port,omitempty"`
	PID           int32  `json:"pid,omitempty"`
	ProcessName   string `json:"process_name,omitempty"`
	CmdlineHead   string `json:"cmdline_head,omitempty"`
	Exe           string `json:"exe,omitempty"`
	User          string `json:"user,omitempty"`
	UID           *int32 `json:"uid,omitempty"`
	Service       string `json:"service,omitempty"`
	ContainerID   string `json:"container_id,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	Cgroup        string `json:"cgroup,omitempty"`
	FirstSeenNS   int64  `json:"first_seen_unix_ns"`
	LastSeenNS    int64  `json:"last_seen_unix_ns"`
}

// FlowItem is one entry of the rolling /network/flows window. Live is
// false once the socket disappeared from a sample; the entry is retained
// for Config.WindowS after last_seen so late-arriving NetFlow records
// still resolve.
type FlowItem struct {
	FlowID        string `json:"flow_id"`
	DirectionHint string `json:"direction_hint,omitempty"` // egress | ingress | unknown
	Live          bool   `json:"live"`
	SocketItem
}

// Status is the collector's envelope-level condition, composed into every
// response by the HTTP layer.
type Status struct {
	Source   string
	Stale    bool
	Partial  bool
	Warnings []string
}

// entry is the internal cache record behind one 5-tuple+pid key.
type entry struct {
	item FlowItem
	live bool
}

type key struct {
	proto  string
	laddr  string
	lport  uint32
	raddr  string
	rport  uint32
	pid    int32
}

// Collector owns the sampled socket table + rolling history.
type Collector struct {
	cfg      Config
	sampler  Sampler
	procs    ProcResolver
	hostname string

	// attrHint, when non-empty, names the missing privilege behind
	// unattributed sockets and the fix (Linux caps — see caps_linux.go).
	// Set by New only; NewWithDeps leaves it empty so tests stay
	// platform-independent.
	attrHint string

	mu           sync.RWMutex
	entries      map[key]*entry
	procCache    map[int32]ProcInfo
	lastAttempt  time.Time
	lastOK       time.Time
	lastErr      error
	unattributed int // sockets with pid==0 or failed enrichment in last sample
}

// New returns a Collector using the platform sampler (gopsutil).
func New(cfg Config) *Collector {
	c := NewWithDeps(cfg, newGopsutilSampler(), newGopsutilProcs())
	c.attrHint = attributionHint()
	return c
}

// NewWithDeps injects the sampler and process resolver — the test seam.
func NewWithDeps(cfg Config, s Sampler, p ProcResolver) *Collector {
	if cfg.PollIntervalS <= 0 {
		cfg.PollIntervalS = 10
	}
	if cfg.WindowS <= 0 {
		cfg.WindowS = 300
	}
	if cfg.CmdlineMaxBytes <= 0 {
		cfg.CmdlineMaxBytes = 240
	}
	host, _ := os.Hostname()
	return &Collector{
		cfg:      cfg,
		sampler:  s,
		procs:    p,
		hostname: host,
		entries:  map[key]*entry{},
	}
}

// Hostname returns the node hostname captured at construction.
func (c *Collector) Hostname() string { return c.hostname }

// WindowS returns the configured retention window in seconds.
func (c *Collector) WindowS() int { return c.cfg.WindowS }

// Run samples on the configured cadence until ctx is done. Start once,
// from the server's Run — read endpoints also refresh on demand via
// SampleIfOlder so the first request after startup isn't blind.
func (c *Collector) Run(done <-chan struct{}) {
	if c.attrHint != "" {
		// Surface the privilege gap in the journal at startup, not only in
		// response envelopes nobody may be reading yet.
		slog.Warn("network attribution will be incomplete", "hint", c.attrHint)
	}
	t := time.NewTicker(time.Duration(c.cfg.PollIntervalS) * time.Second)
	defer t.Stop()
	c.SampleIfOlder(0)
	for {
		select {
		case <-done:
			return
		case <-t.C:
			c.SampleIfOlder(time.Second)
		}
	}
}

// SampleIfOlder re-samples unless the last attempt is younger than d.
// Callers on the request path use ~2s to bound per-request cost while
// keeping /network/resolve near-real-time.
func (c *Collector) SampleIfOlder(d time.Duration) {
	c.mu.RLock()
	fresh := time.Since(c.lastAttempt) < d && !c.lastAttempt.IsZero()
	c.mu.RUnlock()
	if fresh {
		return
	}
	now := time.Now()
	raws, err := c.sampler.Sample()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastAttempt = now
	if err != nil {
		// Keep the existing table — closed-socket history is still
		// useful — and let Status() surface stale=true.
		c.lastErr = err
		return
	}
	c.lastErr = nil
	c.lastOK = now
	c.ingestLocked(raws, now)
}

// ingestLocked merges one sample into the entry table. Caller holds mu.
func (c *Collector) ingestLocked(raws []RawConn, now time.Time) {
	nowNS := now.UnixNano()
	seen := map[key]bool{}
	procCache := map[int32]ProcInfo{}
	procErr := map[int32]bool{}
	unattributed := 0

	// TCP listen ports feed the ingress/egress direction heuristic.
	listen := map[uint32]bool{}
	for _, r := range raws {
		if r.Proto == "tcp" && r.State == "listen" {
			listen[r.LocalPort] = true
		}
	}

	for _, r := range raws {
		k := key{r.Proto, normAddr(r.LocalAddr), r.LocalPort, normAddr(r.RemoteAddr), r.RemotePort, r.PID}
		if seen[k] {
			continue // dup rows for multi-fd sockets
		}
		seen[k] = true
		if e, ok := c.entries[k]; ok {
			e.item.State = r.State
			e.item.LastSeenNS = nowNS
			e.item.Live = true
			e.live = true
			if r.PID == 0 && !kernelOwned(r) {
				unattributed++
			}
			continue
		}
		item := FlowItem{
			Live: true,
			SocketItem: SocketItem{
				Proto:       r.Proto,
				State:       r.State,
				LocalAddr:   k.laddr,
				LocalPort:   r.LocalPort,
				RemoteAddr:  k.raddr,
				RemotePort:  r.RemotePort,
				PID:         r.PID,
				FirstSeenNS: nowNS,
				LastSeenNS:  nowNS,
			},
		}
		item.DirectionHint = direction(r, listen)
		if r.PID > 0 {
			info, ok := procCache[r.PID]
			if !ok && !procErr[r.PID] {
				var err error
				info, err = c.procs.Info(r.PID)
				if err != nil {
					procErr[r.PID] = true
				} else {
					procCache[r.PID] = info
				}
			}
			if procErr[r.PID] {
				unattributed++
			} else {
				item.ProcessName = info.Name
				item.Exe = info.Exe
				item.User = info.User
				item.UID = info.UID
				item.CmdlineHead = RedactCmdline(info.CmdlineRaw, c.cfg.CmdlineMaxBytes)
				item.Cgroup = info.Cgroup
				item.Service = info.Service
				item.ContainerID = info.ContainerID
				item.ContainerName = info.ContainerName
			}
		} else if !kernelOwned(r) {
			unattributed++
		}
		item.FlowID = flowID(k, nowNS)
		c.entries[k] = &entry{item: item, live: true}
	}

	// Anything not in this sample is closed; retain within the window.
	cutoff := nowNS - int64(c.cfg.WindowS)*int64(time.Second)
	for k, e := range c.entries {
		if !seen[k] {
			e.live = false
			e.item.Live = false
		}
		if !e.live && e.item.LastSeenNS < cutoff {
			delete(c.entries, k)
		}
	}
	c.unattributed = unattributed
}

// Status reports the envelope-level condition of the collector.
func (c *Collector) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := Status{Source: c.sampler.Source(), Warnings: []string{}}
	if c.lastErr != nil {
		st.Stale = true
		st.Warnings = append(st.Warnings, fmt.Sprintf("last sample failed: %v", c.lastErr))
	}
	staleAfter := time.Duration(3*c.cfg.PollIntervalS) * time.Second
	if c.lastOK.IsZero() || time.Since(c.lastOK) > staleAfter {
		st.Stale = true
	}
	if c.unattributed > 0 {
		st.Partial = true
		msg := fmt.Sprintf("%d socket(s) lack process attribution", c.unattributed)
		if c.attrHint != "" {
			msg += ": " + c.attrHint
		} else {
			msg += " (agent may lack privilege, or the owning process exited mid-sample)"
		}
		st.Warnings = append(st.Warnings, msg)
	}
	return st
}

// SocketFilter narrows GET /network/sockets.
type SocketFilter struct {
	State string
	Proto string
	Port  uint32 // matches local or remote port
	PID   int32
	Limit int
}

// Sockets returns currently-live sockets, newest last_seen first.
func (c *Collector) Sockets(f SocketFilter) []SocketItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []SocketItem
	for _, e := range c.entries {
		if !e.live {
			continue
		}
		it := e.item
		if f.State != "" && it.State != f.State {
			continue
		}
		if f.Proto != "" && it.Proto != f.Proto {
			continue
		}
		if f.Port != 0 && it.LocalPort != f.Port && it.RemotePort != f.Port {
			continue
		}
		if f.PID != 0 && it.PID != f.PID {
			continue
		}
		out = append(out, it.SocketItem)
	}
	sortSockets(out)
	return capSockets(out, f.Limit)
}

// FlowFilter narrows GET /network/flows.
type FlowFilter struct {
	SinceNS    int64
	Proto      string
	LocalPort  uint32
	RemoteAddr string
	PID        int32
	Limit      int
}

// Flows returns the rolling window (live + recently closed), newest
// last_seen first.
func (c *Collector) Flows(f FlowFilter) []FlowItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	remote := normAddr(f.RemoteAddr)
	var out []FlowItem
	for _, e := range c.entries {
		it := e.item
		if f.SinceNS != 0 && it.LastSeenNS < f.SinceNS {
			continue
		}
		if f.Proto != "" && it.Proto != f.Proto {
			continue
		}
		if f.LocalPort != 0 && it.LocalPort != f.LocalPort {
			continue
		}
		if remote != "" && it.RemoteAddr != remote {
			continue
		}
		if f.PID != 0 && it.PID != f.PID {
			continue
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenNS != out[j].LastSeenNS {
			return out[i].LastSeenNS > out[j].LastSeenNS
		}
		return out[i].FlowID < out[j].FlowID
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

func sortSockets(s []SocketItem) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].LastSeenNS != s[j].LastSeenNS {
			return s[i].LastSeenNS > s[j].LastSeenNS
		}
		if s[i].Proto != s[j].Proto {
			return s[i].Proto < s[j].Proto
		}
		if s[i].LocalPort != s[j].LocalPort {
			return s[i].LocalPort < s[j].LocalPort
		}
		return s[i].PID < s[j].PID
	})
}

func capSockets(s []SocketItem, limit int) []SocketItem {
	if limit > 0 && len(s) > limit {
		return s[:limit]
	}
	return s
}

// kernelOwned reports whether a socket in this state never has an owning
// process: after the fd closes (time_wait) or before accept(2) creates
// one (syn_recv), the kernel holds the socket alone, so pid==0 there is
// structural — even root sees no owner. Counting these as unattributed
// kept `partial: true` permanently on any node with connection churn
// and pointed operators at a privilege problem that didn't exist.
func kernelOwned(r RawConn) bool {
	return r.Proto == "tcp" && (r.State == "time_wait" || r.State == "syn_recv")
}

// direction classifies a socket at first sight. Sampling can't observe
// the SYN, so this is a hint: a connected socket whose local port is also
// a listening port on this node is inbound; other connected sockets are
// outbound; unconnected sockets are unknown.
func direction(r RawConn, listen map[uint32]bool) string {
	if r.State == "listen" || r.RemotePort == 0 && r.RemoteAddr == "" {
		return ""
	}
	if r.Proto == "tcp" && listen[r.LocalPort] {
		return "ingress"
	}
	if r.RemoteAddr != "" {
		return "egress"
	}
	return "unknown"
}

// normAddr canonicalizes an address string for stable matching: zone
// stripped, IPv4-mapped IPv6 collapsed, wildcard spellings ("*") kept as
// returned by the OS elsewhere but empty-normalized here.
func normAddr(a string) string {
	if a == "" || a == "*" {
		return ""
	}
	if i := strings.IndexByte(a, '%'); i >= 0 {
		a = a[:i]
	}
	ip := net.ParseIP(a)
	if ip == nil {
		return a
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// isWildcard reports whether a normalized local address is an any-bind.
func isWildcard(a string) bool {
	return a == "" || a == "0.0.0.0" || a == "::"
}

func flowID(k key, firstSeenNS int64) string {
	h := sha1.Sum([]byte(fmt.Sprintf("%s|%s|%d|%s|%d|%d|%d",
		k.proto, k.laddr, k.lport, k.raddr, k.rport, k.pid, firstSeenNS)))
	return "sha1:" + hex.EncodeToString(h[:])
}
