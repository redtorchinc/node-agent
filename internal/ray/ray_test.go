package ray

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A raylet command line as Ray 2.x actually renders it. Note the mixed
// flag styles (--gcs-address vs --node_ip_address) — that inconsistency is
// Ray's, and is the reason parseFlags keeps keys verbatim.
const raylet2x = `/usr/lib/python3.11/site-packages/ray/core/src/ray/raylet/raylet ` +
	`--raylet_socket_name=/tmp/ray/session_2026-08-25_10-00-00_123456_789/sockets/raylet ` +
	`--store_socket_name=/tmp/ray/session_2026-08-25_10-00-00_123456_789/sockets/plasma_store ` +
	`--object_manager_port=0 --min_worker_port=10002 --max_worker_port=19999 ` +
	`--node_manager_port=42345 --node_ip_address=10.10.10.7 ` +
	`--maximum_startup_concurrency=8 ` +
	`--static_resource_list=node:10.10.10.7,1.0,CPU,20,GPU,1,memory,67108864,object_store_memory,33554432 ` +
	`--gcs-address=192.168.50.96:6379 ` +
	`--session-name=session_2026-08-25_10-00-00_123456_789 ` +
	`--metrics-agent-port=55234`

func TestParseFlagsOnRealRayletCmdline(t *testing.T) {
	f := parseFlags(raylet2x)

	tests := map[string]string{
		"gcs-address":       "192.168.50.96:6379",
		"node_ip_address":   "10.10.10.7",
		"session-name":      "session_2026-08-25_10-00-00_123456_789",
		"node_manager_port": "42345",
	}
	for k, want := range tests {
		if got := f[k]; got != want {
			t.Errorf("flags[%q] = %q, want %q", k, got, want)
		}
	}
	// The binary path is not a flag and must not appear.
	if _, ok := f["usr/lib/python3.11/site-packages/ray/core/src/ray/raylet/raylet"]; ok {
		t.Error("parseFlags picked up the binary path as a flag")
	}
}

func TestParseFlagsIgnoresNonFlagsAndBareFlags(t *testing.T) {
	f := parseFlags("/bin/raylet --verbose positional --key=val -x=1 --=empty")

	if got := f["key"]; got != "val" {
		t.Errorf("flags[key] = %q, want val", got)
	}
	if got := f["x"]; got != "1" {
		t.Errorf("flags[x] = %q, want 1 (single-dash should work)", got)
	}
	// --verbose has no '=' so it carries no value; positional isn't a flag.
	if _, ok := f["verbose"]; ok {
		t.Error("bare --verbose should not produce an entry")
	}
	if _, ok := f["positional"]; ok {
		t.Error("positional arg should not produce an entry")
	}
	if _, ok := f[""]; ok {
		t.Error("empty key should be rejected")
	}
}

func TestParseFlagsEmpty(t *testing.T) {
	if got := parseFlags(""); len(got) != 0 {
		t.Errorf("parseFlags(\"\") = %v, want empty", got)
	}
}

func TestParseStaticResourceList(t *testing.T) {
	got := parseStaticResourceList("node:10.10.10.7,1.0,CPU,20,GPU,1,memory,67108864")

	want := map[string]float64{
		"node:10.10.10.7": 1.0,
		"CPU":             20,
		"GPU":             1,
		"memory":          67108864,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d resources %v, want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("resources[%q] = %v, want %v", k, got[k], v)
		}
	}
}

func TestParseStaticResourceListEdgeCases(t *testing.T) {
	t.Run("empty yields nil", func(t *testing.T) {
		if got := parseStaticResourceList(""); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("trailing unpaired name is ignored, not guessed at", func(t *testing.T) {
		got := parseStaticResourceList("CPU,8,GPU")
		if len(got) != 1 || got["CPU"] != 8 {
			t.Errorf("got %v, want only CPU=8", got)
		}
		if _, present := got["GPU"]; present {
			t.Error("GPU had no value and must be absent, not 0 — a 0-GPU claim would be worse than silence")
		}
	})
	t.Run("unparseable value is skipped, later pairs still read", func(t *testing.T) {
		got := parseStaticResourceList("CPU,eight,GPU,2")
		if got["GPU"] != 2 {
			t.Errorf("got %v, want GPU=2", got)
		}
		if _, present := got["CPU"]; present {
			t.Error("CPU value was unparseable and must be absent")
		}
	})
	t.Run("no valid pairs yields nil", func(t *testing.T) {
		if got := parseStaticResourceList("CPU,eight"); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("fractional GPU shares", func(t *testing.T) {
		if got := parseStaticResourceList("GPU,0.5"); got["GPU"] != 0.5 {
			t.Errorf("got %v, want GPU=0.5", got)
		}
	})
}

func TestSessionDirFromFlags(t *testing.T) {
	f := parseFlags(raylet2x)
	got := sessionDirFromFlags(f)
	want := "/tmp/ray/session_2026-08-25_10-00-00_123456_789"
	if got != want {
		t.Errorf("sessionDirFromFlags = %q, want %q", got, want)
	}
}

func TestSessionDirFromFlagsHonoursCustomTempDir(t *testing.T) {
	// A non-default --temp-dir must be picked up, which is why the dir is
	// derived from the socket path rather than assuming /tmp/ray.
	f := parseFlags(`raylet --raylet_socket_name=/mnt/fast/ray/session_abc/sockets/raylet`)
	if got := sessionDirFromFlags(f); got != "/mnt/fast/ray/session_abc" {
		t.Errorf("got %q, want /mnt/fast/ray/session_abc", got)
	}
}

func TestSessionDirFromFlagsNoSocketFlags(t *testing.T) {
	if got := sessionDirFromFlags(parseFlags("raylet --node_ip_address=1.2.3.4")); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRoleFor(t *testing.T) {
	tests := []struct {
		name        string
		raylet, gcs int
		want        string
	}{
		{"head runs gcs_server", 100, 200, "head"},
		{"worker runs raylet only", 100, 0, "worker"},
		{"gcs without raylet still reads as head", 0, 200, "head"},
		{"neither", 0, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roleFor(tt.raylet, tt.gcs); got != tt.want {
				t.Errorf("roleFor(%d, %d) = %q, want %q", tt.raylet, tt.gcs, got, tt.want)
			}
		})
	}
}

func TestSessionFromDiskUsesKnownDir(t *testing.T) {
	// A Ray-emitted POSIX path, deliberately not t.TempDir(): this parser
	// must behave identically on every OS in the build matrix, and an
	// OS-native temp path would hide a windows separator bug.
	const session = "/tmp/ray/session_2026-08-25_11-00-00_1_2"

	name, got := sessionFromDisk(session)
	if name != "session_2026-08-25_11-00-00_1_2" {
		t.Errorf("name = %q", name)
	}
	if got != session {
		t.Errorf("dir = %q, want %q", got, session)
	}
}

// Regression (windows CI): paths from the raylet are POSIX and land verbatim
// on the wire as session_dir. filepath.Dir on a windows build rewrote
// "/tmp/ray/session_x" to "\\tmp\\ray\\session_x"; this package uses
// `path` so the result is slash-form on every OS.
func TestSessionPathsStaySlashFormOnEveryOS(t *testing.T) {
	got := sessionDirFromFlags(parseFlags(raylet2x))
	if strings.ContainsRune(got, '\\') {
		t.Errorf("session dir %q contains a backslash; must stay POSIX on all platforms", got)
	}
	if !strings.HasPrefix(got, "/") {
		t.Errorf("session dir %q should start with /", got)
	}
}

func TestSessionFromDiskRejectsSessionLatest(t *testing.T) {
	// session_latest is a symlink, not a session identity — resolving to it
	// would report a name that's identical on every cluster.
	name, _ := sessionFromDisk("/tmp/ray/session_latest")
	if name == "session_latest" {
		t.Error("session_latest must not be reported as a cluster id")
	}
}

func TestCountAliveNodesNodesSummary(t *testing.T) {
	body := []byte(`{"data":{"summary":[
		{"raylet":{"state":"ALIVE"}},
		{"raylet":{"state":"ALIVE"}},
		{"raylet":{"state":"DEAD"}}
	]}}`)
	n, err := countAliveNodes(body)
	if err != nil {
		t.Fatal(err)
	}
	// The dead node must not inflate the group size.
	if n != 2 {
		t.Errorf("got %d, want 2 (DEAD node must not count)", n)
	}
}

func TestCountAliveNodesEightNodeTPGroup(t *testing.T) {
	// The case from issue #30: 8x GB10 serving one model.
	body := []byte(`{"data":{"summary":[
		{"raylet":{"state":"ALIVE"}},{"raylet":{"state":"ALIVE"}},
		{"raylet":{"state":"ALIVE"}},{"raylet":{"state":"ALIVE"}},
		{"raylet":{"state":"ALIVE"}},{"raylet":{"state":"ALIVE"}},
		{"raylet":{"state":"ALIVE"}},{"raylet":{"state":"ALIVE"}}
	]}}`)
	n, err := countAliveNodes(body)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("got %d, want 8", n)
	}
}

func TestCountAliveNodesTopLevelState(t *testing.T) {
	body := []byte(`{"data":{"summary":[{"state":"ALIVE"},{"state":"ALIVE"}]}}`)
	n, err := countAliveNodes(body)
	if err != nil || n != 2 {
		t.Errorf("got (%d, %v), want (2, nil)", n, err)
	}
}

func TestCountAliveNodesClusterStatusShape(t *testing.T) {
	body := []byte(`{"data":{"clusterStatus":{"data":{"clusterStatus":{"active_nodes":{"gb10":7,"head":1}}}}}}`)
	n, err := countAliveNodes(body)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("got %d, want 8", n)
	}
}

func TestCountAliveNodesRejectsUnrecognised(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":{}}`, `{"data":{"summary":[]}}`, `not json`} {
		if _, err := countAliveNodes([]byte(body)); err == nil {
			t.Errorf("countAliveNodes(%q) = nil error, want error", body)
		}
	}
}

func TestFetchAliveNodesFallsThroughToSecondEndpoint(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.RequestURI())
		if r.URL.Path == "/nodes" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"clusterStatus":{"data":{"clusterStatus":{"active_nodes":{"gb10":8}}}}}}`))
	}))
	defer srv.Close()

	n, err := fetchAliveNodes(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if n != 8 {
		t.Errorf("got %d, want 8", n)
	}
	if len(hits) != 2 {
		t.Errorf("hit %d endpoints (%v), want 2 — the first should have been tried and failed", len(hits), hits)
	}
}

func TestFetchAliveNodesErrorsWhenBothEndpointsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := fetchAliveNodes(context.Background(), srv.URL); err == nil {
		t.Error("want an error when every endpoint fails")
	}
}

func TestDisabledProbeReturnsNil(t *testing.T) {
	d := New(Config{Enabled: "false"})
	if got := d.Probe(context.Background()); got != nil {
		t.Errorf("Probe with enabled:false = %+v, want nil", got)
	}
	if got := d.Refresh(context.Background()); got != nil {
		t.Errorf("Refresh with enabled:false = %+v, want nil", got)
	}
}

func TestNewFillsDefaults(t *testing.T) {
	d := New(Config{})
	if d.cfg.Enabled != "auto" {
		t.Errorf("Enabled = %q, want auto", d.cfg.Enabled)
	}
	if d.cfg.ProbeIntervalS != 30 {
		t.Errorf("ProbeIntervalS = %d, want 30", d.cfg.ProbeIntervalS)
	}
}

func TestDisabledHelper(t *testing.T) {
	tests := map[string]bool{"false": true, "FALSE": true, "auto": false, "true": false, "": false}
	for in, want := range tests {
		if got := (Config{Enabled: in}).Disabled(); got != want {
			t.Errorf("Config{Enabled:%q}.Disabled() = %v, want %v", in, got, want)
		}
	}
}

func TestWithStalenessMarksOldReading(t *testing.T) {
	d := New(Config{ProbeIntervalS: 10})

	fresh := d.withStaleness(&Info{Running: true, LastProbe: time.Now().Unix()})
	if fresh.Stale {
		t.Error("a just-taken reading must not be stale")
	}
	if fresh.ProbeIntervalS != 10 {
		t.Errorf("ProbeIntervalS = %d, want 10", fresh.ProbeIntervalS)
	}

	// Older than 3x the interval.
	old := d.withStaleness(&Info{Running: true, LastProbe: time.Now().Unix() - 31})
	if !old.Stale {
		t.Error("a reading older than 3x probe_interval_s must be stale")
	}
}

func TestHeadEndpointHintNilSafe(t *testing.T) {
	var i *Info
	if got := i.HeadEndpointHint(); got != "" {
		t.Errorf("nil Info hint = %q, want empty", got)
	}
	i = &Info{GCSAddress: "10.0.0.1:6379"}
	if got := i.HeadEndpointHint(); got != "10.0.0.1:6379" {
		t.Errorf("got %q", got)
	}
}

// A worker must not spend a dashboard timeout, and must say why
// alive_nodes is missing rather than leaving it unexplained.
func TestFillClusterStateSkipsDashboardOnWorker(t *testing.T) {
	d := New(Config{})
	info := &Info{Running: true, Role: "worker", NodeIP: "10.10.10.7"}
	d.fillClusterState(context.Background(), info)

	if info.AliveNodes != nil {
		t.Errorf("AliveNodes = %v, want nil on a worker", *info.AliveNodes)
	}
	if info.DashboardURL != "" {
		t.Errorf("DashboardURL = %q, want empty (no fetch attempted)", info.DashboardURL)
	}
	if info.Error == "" {
		t.Error("want an Error explaining why alive_nodes is absent")
	}
}

func TestFillClusterStateUsesConfiguredDashboardOnWorker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"summary":[{"raylet":{"state":"ALIVE"}},{"raylet":{"state":"ALIVE"}}]}}`))
	}))
	defer srv.Close()

	d := New(Config{DashboardURL: srv.URL})
	info := &Info{Running: true, Role: "worker"}
	d.fillClusterState(context.Background(), info)

	if info.AliveNodes == nil {
		t.Fatalf("AliveNodes = nil, want 2 (explicit DashboardURL should override the worker skip); err=%q", info.Error)
	}
	if *info.AliveNodes != 2 {
		t.Errorf("AliveNodes = %d, want 2", *info.AliveNodes)
	}
	if info.Source != "cmdline+dashboard" {
		t.Errorf("Source = %q, want cmdline+dashboard", info.Source)
	}
	if info.Error != "" {
		t.Errorf("Error = %q, want cleared on success", info.Error)
	}
}

func TestFillClusterStateRecordsDashboardFailure(t *testing.T) {
	d := New(Config{DashboardURL: "http://127.0.0.1:1"}) // nothing listening
	info := &Info{Running: true, Role: "head"}
	d.fillClusterState(context.Background(), info)

	if info.AliveNodes != nil {
		t.Error("AliveNodes must stay nil when the dashboard is unreachable")
	}
	if info.Error == "" {
		t.Error("want an Error recording the dashboard failure")
	}
	if info.DashboardURL == "" {
		t.Error("want DashboardURL recorded even on failure, for diagnosis")
	}
	// Crucially the block survives: role and gcs_address still group the node.
	if !info.Running {
		t.Error("a dashboard failure must not invalidate the block")
	}
}

// Regression: the derived dashboard URL must be localhost, not NodeIP.
// Deriving it from NodeIP meant a node whose registered Ray IP isn't
// locally routable burned the full timeout on both endpoints every refresh
// — caught by an end-to-end smoke test against a fake raylet advertising
// --node_ip_address=10.10.10.7.
func TestFillClusterStateDerivesLocalhostDashboard(t *testing.T) {
	d := New(Config{})
	info := &Info{Running: true, Role: "head", NodeIP: "10.10.10.7"}
	d.fillClusterState(context.Background(), info)

	if got := info.DashboardURL; got != "http://127.0.0.1:8265" {
		t.Errorf("DashboardURL = %q, want http://127.0.0.1:8265 (never NodeIP — Ray binds the dashboard to localhost by default)", got)
	}
}

// gcs_address must come only from the raylet, never be synthesized from the
// gcs_server's own flags. Grouping is string equality across members, and a
// head that computed its own "10.10.10.7:6379" would fail to join workers
// told "192.168.50.96:6379". Reporting nothing beats reporting a value that
// doesn't join.
func TestGCSAddressComesOnlyFromRayletCmdline(t *testing.T) {
	// A gcs_server-only node: no raylet cmdline to read.
	flags := parseFlags("")
	if got := firstNonEmpty(flags["gcs-address"], flags["gcs_address"], flags["redis_address"]); got != "" {
		t.Errorf("gcs_address = %q with no raylet cmdline, want empty", got)
	}
	// And the flag names a head's own gcs_server would carry must NOT be
	// consulted for it.
	gcs := parseFlags("gcs_server --gcs_server_port=6379 --node-ip-address=10.10.10.7")
	if got := firstNonEmpty(gcs["gcs-address"], gcs["gcs_address"], gcs["redis_address"]); got != "" {
		t.Errorf("gcs_address = %q, want empty — must not be built from gcs_server flags", got)
	}
}
