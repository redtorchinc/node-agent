package netown

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeSampler serves scripted samples; each Sample() call pops the next.
type fakeSampler struct {
	samples [][]RawConn
	errs    []error
	calls   int
}

func (f *fakeSampler) Sample() ([]RawConn, error) {
	i := f.calls
	f.calls++
	if i >= len(f.samples) {
		i = len(f.samples) - 1
	}
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return f.samples[i], err
}
func (f *fakeSampler) Source() string { return "fake" }

type fakeProcs map[int32]ProcInfo

func (f fakeProcs) Info(pid int32) (ProcInfo, error) {
	if p, ok := f[pid]; ok {
		return p, nil
	}
	return ProcInfo{}, errors.New("no such pid")
}

func uidPtr(v int32) *int32 { return &v }

func testConns() []RawConn {
	return []RawConn{
		{Proto: "tcp", LocalAddr: "0.0.0.0", LocalPort: 8000, State: "listen", PID: 100},
		{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 8000, RemoteAddr: "10.0.0.9", RemotePort: 51000, State: "established", PID: 100},
		{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 40000, RemoteAddr: "93.184.216.34", RemotePort: 443, State: "established", PID: 200},
		{Proto: "udp", LocalAddr: "*", LocalPort: 5353, PID: 300},
		// Duplicate row (multi-fd) — must dedupe.
		{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 40000, RemoteAddr: "93.184.216.34", RemotePort: 443, State: "established", PID: 200},
		// Unattributed socket → partial.
		{Proto: "tcp", LocalAddr: "10.0.0.5", LocalPort: 22, RemoteAddr: "10.0.0.7", RemotePort: 60000, State: "established", PID: 0},
	}
}

func testProcs() fakeProcs {
	return fakeProcs{
		100: {Name: "vllm", Exe: "/opt/venv/bin/python", User: "svc", UID: uidPtr(990),
			CmdlineRaw: []string{"/opt/venv/bin/python", "-m", "vllm.entrypoints.openai.api_server", "--api-key", "sk-verysecret"},
			Cgroup:     "/system.slice/rt-vllm-example.service", Service: "rt-vllm-example.service"},
		200: {Name: "curl", Exe: "/usr/bin/curl", User: "alice", UID: uidPtr(1000),
			CmdlineRaw: []string{"curl", "https://example.com/"}},
		300: {Name: "mdns", Exe: "/usr/sbin/mdns", User: "root", UID: uidPtr(0)},
	}
}

func newTestCollector(t *testing.T, fs *fakeSampler) *Collector {
	t.Helper()
	c := NewWithDeps(Config{PollIntervalS: 10, WindowS: 300, CmdlineMaxBytes: 240}, fs, testProcs())
	c.SampleIfOlder(0)
	return c
}

func TestSockets_OwnershipAndDedup(t *testing.T) {
	c := newTestCollector(t, &fakeSampler{samples: [][]RawConn{testConns()}})

	all := c.Sockets(SocketFilter{})
	if len(all) != 5 { // 6 raw − 1 duplicate
		t.Fatalf("got %d sockets, want 5: %+v", len(all), all)
	}
	var egress *SocketItem
	for i := range all {
		if all[i].RemotePort == 443 {
			egress = &all[i]
		}
	}
	if egress == nil {
		t.Fatal("egress socket missing")
	}
	if egress.ProcessName != "curl" || egress.User != "alice" || *egress.UID != 1000 {
		t.Errorf("owner enrichment wrong: %+v", egress)
	}
	if egress.LocalAddr != "10.0.0.5" {
		t.Errorf("local addr = %q", egress.LocalAddr)
	}
}

func TestSockets_Filters(t *testing.T) {
	c := newTestCollector(t, &fakeSampler{samples: [][]RawConn{testConns()}})

	if got := c.Sockets(SocketFilter{Proto: "udp"}); len(got) != 1 || got[0].LocalPort != 5353 {
		t.Errorf("proto=udp: %+v", got)
	}
	if got := c.Sockets(SocketFilter{State: "listen"}); len(got) != 1 || got[0].PID != 100 {
		t.Errorf("state=listen: %+v", got)
	}
	if got := c.Sockets(SocketFilter{Port: 443}); len(got) != 1 || got[0].PID != 200 {
		t.Errorf("port=443 (remote match): %+v", got)
	}
	if got := c.Sockets(SocketFilter{PID: 100}); len(got) != 2 {
		t.Errorf("pid=100: want 2, got %+v", got)
	}
	if got := c.Sockets(SocketFilter{Limit: 2}); len(got) != 2 {
		t.Errorf("limit=2: got %d", len(got))
	}
}

func TestFlows_RetainsClosedSockets_DirectionHints(t *testing.T) {
	first := testConns()
	second := testConns()[:2] // curl + udp + unattributed disappear
	fs := &fakeSampler{samples: [][]RawConn{first, second}}
	c := newTestCollector(t, fs)
	c.SampleIfOlder(0) // ingest second sample (throttle 0 → always)

	flows := c.Flows(FlowFilter{})
	if len(flows) != 5 {
		t.Fatalf("flows must retain closed entries in window: got %d", len(flows))
	}
	byPort := map[uint32]FlowItem{}
	for _, f := range flows {
		if f.RemotePort != 0 {
			byPort[f.RemotePort] = f
		}
	}
	closed := byPort[443]
	if closed.Live {
		t.Error("socket gone from sample must be live=false")
	}
	if closed.DirectionHint != "egress" {
		t.Errorf("outbound 443 direction = %q, want egress", closed.DirectionHint)
	}
	inbound := byPort[51000]
	if !inbound.Live {
		t.Error("still-sampled socket must stay live")
	}
	if inbound.DirectionHint != "ingress" {
		t.Errorf("conn on listening port 8000 direction = %q, want ingress", inbound.DirectionHint)
	}
	if !strings.HasPrefix(closed.FlowID, "sha1:") || len(closed.FlowID) != len("sha1:")+40 {
		t.Errorf("flow_id shape: %q", closed.FlowID)
	}
}

func TestFlows_SinceAndRemoteFilter(t *testing.T) {
	c := newTestCollector(t, &fakeSampler{samples: [][]RawConn{testConns()}})
	if got := c.Flows(FlowFilter{RemoteAddr: "93.184.216.34"}); len(got) != 1 || got[0].PID != 200 {
		t.Errorf("remote_addr filter: %+v", got)
	}
	future := time.Now().Add(time.Hour).UnixNano()
	if got := c.Flows(FlowFilter{SinceNS: future}); len(got) != 0 {
		t.Errorf("since in future must return nothing: %+v", got)
	}
}

func TestStatus_PartialAndStale(t *testing.T) {
	fs := &fakeSampler{
		samples: [][]RawConn{testConns(), testConns()},
		errs:    []error{nil, errors.New("permission denied")},
	}
	c := newTestCollector(t, fs)
	st := c.Status()
	if !st.Partial {
		t.Error("unattributed pid=0 socket must set partial")
	}
	if st.Stale {
		t.Error("fresh successful sample must not be stale")
	}
	c.SampleIfOlder(0) // second sample errors
	st = c.Status()
	if !st.Stale {
		t.Error("failed sample must set stale")
	}
	if len(st.Warnings) == 0 {
		t.Error("failed sample must carry a warning")
	}
	// History must survive the failed sample.
	if got := c.Flows(FlowFilter{}); len(got) == 0 {
		t.Error("entry table must be retained across a failed sample")
	}
}

func TestNormAddr(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"*":                    "",
		"0.0.0.0":              "0.0.0.0",
		"::":                   "::",
		"::ffff:10.0.0.5":      "10.0.0.5",
		"fe80::1%en0":          "fe80::1",
		"10.0.0.5":             "10.0.0.5",
		"2001:db8:0:0:0:0:0:1": "2001:db8::1",
	}
	for in, want := range cases {
		if got := normAddr(in); got != want {
			t.Errorf("normAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
