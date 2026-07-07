package netown

import (
	"strings"
	"testing"
	"time"
)

func resolveCollector(t *testing.T) *Collector {
	t.Helper()
	return newTestCollector(t, &fakeSampler{samples: [][]RawConn{testConns()}})
}

func TestResolve_ExactLive(t *testing.T) {
	c := resolveCollector(t)
	m := c.Resolve(Query{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 40000, RemoteAddr: "93.184.216.34", RemotePort: 443})
	if m.Status != "matched" || m.Confidence != confExactLive {
		t.Fatalf("exact live: %+v", m)
	}
	if m.Owner == nil || m.Owner.ProcessName != "curl" {
		t.Errorf("owner: %+v", m.Owner)
	}
	if m.Socket == nil || !m.Socket.Live {
		t.Errorf("socket echo: %+v", m.Socket)
	}
}

func TestResolve_ExactFromCache(t *testing.T) {
	fs := &fakeSampler{samples: [][]RawConn{testConns(), testConns()[:2]}}
	c := newTestCollector(t, fs)
	c.SampleIfOlder(0) // curl socket now closed

	m := c.Resolve(Query{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 40000, RemoteAddr: "93.184.216.34", RemotePort: 443})
	if m.Status != "probable" || m.Confidence != confExactCache {
		t.Fatalf("cache hit: %+v", m)
	}
	if m.Socket.Live {
		t.Error("cache hit must echo live=false")
	}
}

func TestResolve_ListenCoversInbound(t *testing.T) {
	c := resolveCollector(t)
	// Inbound flow to port 8000 from a peer we never saw a per-connection
	// socket for — the wildcard listen socket must cover it.
	m := c.Resolve(Query{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 8000, RemoteAddr: "172.16.0.99", RemotePort: 55555})
	if m.Status != "probable" || m.Confidence != confListen {
		t.Fatalf("listen tier: %+v", m)
	}
	if m.Owner.Service != "rt-vllm-example.service" {
		t.Errorf("service unit: %+v", m.Owner)
	}
}

func TestResolve_UDPPortOwner(t *testing.T) {
	c := resolveCollector(t)
	m := c.Resolve(Query{Proto: "udp", LocalAddr: "10.0.0.5", LocalPort: 5353, RemoteAddr: "224.0.0.251", RemotePort: 5353})
	if m.Status != "probable" || m.Confidence != confUDPPort {
		t.Fatalf("udp tier: %+v", m)
	}
}

func TestResolve_NotFound(t *testing.T) {
	c := resolveCollector(t)
	m := c.Resolve(Query{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 1, RemoteAddr: "8.8.8.8", RemotePort: 53})
	if m.Status != "not_found" || m.Confidence != 0 || m.Owner != nil {
		t.Fatalf("not_found: %+v", m)
	}
	// Stale observed_at gets an explanatory reason.
	old := time.Now().Add(-time.Hour).UnixNano()
	m = c.Resolve(Query{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 1, RemoteAddr: "8.8.8.8", RemotePort: 53, ObservedAtNS: old})
	if m.Status != "not_found" {
		t.Fatalf("stale observed_at: %+v", m)
	}
	if want := "outside the 300s retention window"; !strings.Contains(m.Reason, want) {
		t.Errorf("reason %q must mention %q", m.Reason, want)
	}
}

func TestResolve_AmbiguousDowngrades(t *testing.T) {
	conns := []RawConn{
		{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 9000, RemoteAddr: "10.0.0.9", RemotePort: 41000, State: "established", PID: 100},
		{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 9000, RemoteAddr: "10.0.0.9", RemotePort: 42000, State: "established", PID: 200},
	}
	c := newTestCollector(t, &fakeSampler{samples: [][]RawConn{conns}})
	// Port-only tier: both PIDs own local port 9000 → ambiguous.
	m := c.Resolve(Query{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 9000, RemoteAddr: "203.0.113.7", RemotePort: 443})
	if m.Status != "ambiguous" {
		t.Fatalf("want ambiguous, got %+v", m)
	}
	if m.Confidence >= confPortOnly {
		t.Errorf("ambiguity must lower confidence: %v", m.Confidence)
	}
}

// Normalization: IPv4-mapped IPv6 in the query must match the v4 socket.
func TestResolve_QueryNormalization(t *testing.T) {
	c := resolveCollector(t)
	m := c.Resolve(Query{Proto: "tcp", LocalAddr: "::ffff:10.0.0.5", LocalPort: 40000, RemoteAddr: "::ffff:93.184.216.34", RemotePort: 443})
	if m.Status != "matched" {
		t.Fatalf("v4-mapped query must match v4 socket: %+v", m)
	}
}
