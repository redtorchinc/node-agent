// Package ray reports this node's membership in a Ray cluster.
//
// Why this exists (issue #30): the fleet serves large models as N-way
// tensor-parallel over Ray — e.g. one model across 8× GB10 fronted by a Ray
// head. Only the head exposes an OpenAI-compatible endpoint, so to any
// consumer looking at /health the other seven nodes are indistinguishable
// from idle machines with no inference platform. They were being rendered
// as "offline". The backend needs to see that those eight nodes are one
// serving group.
//
// # Grouping key
//
// gcs_address is the reliable group identity: every node in a Ray cluster
// points at the same GCS, so the backend groups a serving unit with
// `GROUP BY ray.gcs_address` and picks the `role: "head"` member as the
// addressable endpoint. cluster_id (Ray's session name) corroborates it.
//
// Note that alive_nodes is a convenience, not the grouping mechanism —
// counting fleet rows that share a gcs_address is strictly more reliable
// than what any single node can see, and doesn't depend on Ray's dashboard
// API staying shaped the way it is today. Treat a nil alive_nodes as
// "ask the fleet", not as an error.
//
// # Detection is cheap and read-only
//
// Everything except alive_nodes comes out of the raylet's own command line,
// which Ray populates with exactly what we need:
//
//	--gcs-address=192.168.50.96:6379
//	--session-name=session_2026-08-25_10-00-00_123456_789
//	--node_ip_address=10.10.10.7
//	--static_resource_list=node:10.10.10.7,1.0,CPU,20,GPU,1,memory,...
//
// So there is no `ray status` shell-out on the /health path. That matters:
// `ray status` is a Python CLI that costs seconds, and v0.2.8 already had to
// strip a 5s dead-weight out of darwin /health. Ray also mixes `-` and `_`
// flag styles between versions, so both spellings are accepted.
//
// Role comes from which daemons are running: gcs_server marks the head,
// raylet alone marks a worker.
//
// Best-effort throughout, and the block is omitted from /health entirely
// when Ray isn't running — same contract as the rdma block. This package
// deliberately adds NO degraded reason: a node being a TP worker is a
// topology fact, not a health problem, and docs/degraded-reasons.md is a
// cross-repo contract that must not grow silently.
package ray

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// Daemon process names. gopsutil reads /proc/<pid>/comm on Linux, which is
// capped at 15 characters — both of these fit, so no truncation handling is
// needed. Ray's per-task workers rename themselves to "ray::<TaskName>";
// those are deliberately not matched, since they come and go with the
// workload and say nothing about cluster membership.
const (
	rayletProc = "raylet"
	gcsProc    = "gcs_server"
)

// cacheTTL matches the databases probe: longer than the case-manager's 30s
// response cache so backend polls land warm. Cluster membership changes on
// the order of a deploy, not a poll.
const cacheTTL = 30 * time.Second

// dashboardTimeout bounds the only network call in this package. Kept tight
// because a wedged dashboard must not become /health latency; the TTL cache
// plus the keep-warm ticker mean a request almost never pays it.
const dashboardTimeout = 1500 * time.Millisecond

// defaultDashboardPort is Ray's dashboard default.
const defaultDashboardPort = "8265"

// Config mirrors the `ray:` block in config.yaml.
type Config struct {
	// Enabled: "auto" (default, detect and report if present), "true"
	// (same as auto — detection is the only mode; there is nothing to force
	// on a node with no Ray), or "false" (skip the probe entirely, so
	// /health has no ray block and capabilities reports ray_supported).
	Enabled string

	// DashboardURL overrides where alive_nodes/resources are read from.
	// Empty means derive it: http://<node_ip>:8265 on a head node, and no
	// fetch at all on a worker (workers don't run a dashboard).
	DashboardURL string

	// ProbeIntervalS is advisory, surfaced on the wire as probe_interval_s
	// so the backend can judge staleness without hardcoding a threshold
	// (the lesson from issue #8).
	ProbeIntervalS int
}

// Disabled reports whether the operator turned the probe off.
func (c Config) Disabled() bool { return strings.EqualFold(c.Enabled, "false") }

// Info is the /health.ray block. Omitted entirely (nil) when Ray isn't
// running on this node, so a consumer can treat presence as "this node is
// in a Ray cluster".
type Info struct {
	// Running is always true when the block is present. It is emitted
	// explicitly so the JSON is self-describing rather than relying on the
	// reader inferring meaning from key presence.
	Running bool `json:"running"`

	// Role is "head" (gcs_server present) or "worker" (raylet only). The
	// head is the member the backend should route inference to.
	Role string `json:"role,omitempty"`

	// GCSAddress is the cluster's GCS endpoint, identical on every member.
	// THIS IS THE GROUPING KEY — see the package doc.
	GCSAddress string `json:"gcs_address,omitempty"`

	// NodeIP is the address this node registered with Ray, which on
	// multi-homed hosts is not necessarily the address /health arrived on.
	NodeIP string `json:"node_ip,omitempty"`

	// ClusterID is Ray's session name (e.g.
	// "session_2026-08-25_10-00-00_123456_789"). Ray generates it on the
	// head and propagates it to workers via GCS, so it is cluster-wide and
	// usable to corroborate GCSAddress. It changes on every cluster
	// restart, which makes it a useful "same cluster incarnation" check.
	ClusterID string `json:"cluster_id,omitempty"`

	// AliveNodes is the cluster member count. A POINTER because nil
	// ("unknown — the dashboard wasn't reachable") and 0 are different
	// claims, and a consumer must not read a failed probe as an empty
	// cluster. Prefer counting fleet rows sharing GCSAddress.
	AliveNodes *int `json:"alive_nodes"`

	// Resources is this node's Ray resource advertisement, parsed from the
	// raylet's --static_resource_list (so it costs nothing). Keys are Ray
	// resource names: "CPU", "GPU", "memory", "node:<ip>", plus any custom
	// resource the operator declared.
	Resources map[string]float64 `json:"resources,omitempty"`

	// RayletPID / GCSPID are for operators correlating with ps output and
	// journal lines; the databases[] block surfaces PIDs for the same
	// reason.
	RayletPID int `json:"raylet_pid,omitempty"`
	GCSPID    int `json:"gcs_pid,omitempty"`

	// SessionDir shows where ClusterID was read from, so a surprising value
	// is auditable rather than mysterious. Derived from the raylet's socket
	// paths, which honours a non-default --temp-dir.
	SessionDir string `json:"session_dir,omitempty"`

	// DashboardURL records the endpoint consulted for AliveNodes — whether
	// it succeeded or not, so a nil AliveNodes can be diagnosed.
	DashboardURL string `json:"dashboard_url,omitempty"`

	// Source names which signals produced this block, following the
	// precedent set by netown's "lsof+netstat": "cmdline", or
	// "cmdline+dashboard" when AliveNodes came back.
	Source string `json:"source,omitempty"`

	// Error explains a partial result (typically why AliveNodes is nil).
	// Never a reason to discard the block: Role and GCSAddress are already
	// enough to group the node.
	Error string `json:"error,omitempty"`

	// LastProbe / ProbeIntervalS / Stale mirror the self-describing
	// staleness contract added to platforms.* in v0.2.2 (issue #8).
	LastProbe      int64 `json:"last_probe,omitempty"`
	ProbeIntervalS int64 `json:"probe_interval_s,omitempty"`
	Stale          bool  `json:"stale,omitempty"`
}

// HeadEndpointHint returns host:port of the Ray head's GCS, or "" when
// unknown. Convenience for callers grouping nodes into serving units.
func (i *Info) HeadEndpointHint() string {
	if i == nil {
		return ""
	}
	return i.GCSAddress
}

// Detector holds the probe cache. One per agent, created by the health
// reporter.
type Detector struct {
	cfg Config

	mu       sync.Mutex
	cached   *Info
	cachedAt time.Time
}

// New builds a Detector. A zero/partial Config is filled with defaults so
// callers constructing one directly (tests) don't have to.
func New(cfg Config) *Detector {
	if cfg.Enabled == "" {
		cfg.Enabled = "auto"
	}
	if cfg.ProbeIntervalS <= 0 {
		cfg.ProbeIntervalS = 30
	}
	return &Detector{cfg: cfg}
}

// Probe returns cached membership info, refreshing when the cache is cold.
// Returns nil when Ray isn't running, or when the operator disabled the
// probe — in both cases /health omits the block.
func (d *Detector) Probe(ctx context.Context) *Info {
	if d.cfg.Disabled() {
		return nil
	}
	d.mu.Lock()
	if d.cached != nil && time.Since(d.cachedAt) < cacheTTL {
		out := *d.cached
		d.mu.Unlock()
		return d.withStaleness(&out)
	}
	d.mu.Unlock()
	return d.Refresh(ctx)
}

// Refresh forces a probe regardless of cache age. Called by the keep-warm
// ticker — Probe() would no-op inside the fresh window and leave the next
// /health paying full cost.
func (d *Detector) Refresh(ctx context.Context) *Info {
	if d.cfg.Disabled() {
		return nil
	}
	info := d.probeUncached(ctx)

	d.mu.Lock()
	d.cached = info
	d.cachedAt = time.Now()
	d.mu.Unlock()

	if info == nil {
		return nil
	}
	out := *info
	return d.withStaleness(&out)
}

// withStaleness stamps the self-describing staleness fields at render time
// rather than probe time, so `stale` reflects the age of the reading the
// caller is actually being handed.
func (d *Detector) withStaleness(i *Info) *Info {
	if i == nil {
		return nil
	}
	i.ProbeIntervalS = int64(d.cfg.ProbeIntervalS)
	if i.LastProbe > 0 && d.cfg.ProbeIntervalS > 0 {
		age := time.Now().Unix() - i.LastProbe
		i.Stale = age > int64(3*d.cfg.ProbeIntervalS)
	}
	return i
}

func (d *Detector) probeUncached(ctx context.Context) *Info {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil
	}

	var (
		rayletPID  int
		gcsPID     int
		rayletArgs string
	)
	for _, p := range procs {
		name, err := p.Name()
		if err != nil {
			continue
		}
		switch name {
		case rayletProc:
			// Keep the first raylet found. A node runs exactly one; a
			// second would mean two Ray instances, which Ray itself does
			// not support on one node.
			if rayletPID == 0 {
				rayletPID = int(p.Pid)
				rayletArgs, _ = p.Cmdline()
			}
		case gcsProc:
			if gcsPID == 0 {
				gcsPID = int(p.Pid)
			}
		}
	}

	// Neither daemon: Ray is not running here. Omit the block.
	if rayletPID == 0 && gcsPID == 0 {
		return nil
	}

	info := &Info{
		Running:   true,
		Role:      roleFor(rayletPID, gcsPID),
		RayletPID: rayletPID,
		GCSPID:    gcsPID,
		Source:    "cmdline",
		LastProbe: time.Now().Unix(),
	}

	// Everything below comes from the RAYLET's command line only, and
	// gcs_address deliberately is never synthesized from gcs_server's own
	// flags even though a head could compute its own ip:port.
	//
	// Grouping depends on string equality of gcs_address across members. The
	// raylet's --gcs-address is the one value every member is handed
	// verbatim, so it is guaranteed to match. Deriving the head's from
	// --node-ip-address + --gcs_server_port could produce a
	// different-but-equivalent string (10.10.10.7:6379 vs the advertised
	// 192.168.50.96:6379) and the head would silently fail to group with its
	// own workers — worse than reporting nothing.
	//
	// Consequence, accepted: a head whose raylet is momentarily dead reports
	// role: "head" with no gcs_address and is ungroupable until the raylet is
	// back. That is a transiently broken node, and honest.
	flags := parseFlags(rayletArgs)
	info.GCSAddress = firstNonEmpty(flags["gcs-address"], flags["gcs_address"], flags["redis_address"])
	info.NodeIP = firstNonEmpty(flags["node_ip_address"], flags["node-ip-address"])
	info.ClusterID = firstNonEmpty(flags["session-name"], flags["session_name"])
	info.SessionDir = sessionDirFromFlags(flags)

	if res := parseStaticResourceList(firstNonEmpty(flags["static_resource_list"], flags["static-resource-list"])); len(res) > 0 {
		info.Resources = res
	}

	// Fall back to the session directory on disk when the raylet didn't
	// carry --session-name (older Ray versions).
	if info.ClusterID == "" {
		if name, dir := sessionFromDisk(info.SessionDir); name != "" {
			info.ClusterID = name
			if info.SessionDir == "" {
				info.SessionDir = dir
			}
		}
	}

	d.fillClusterState(ctx, info)
	return info
}

// fillClusterState adds AliveNodes from Ray's dashboard API. Head-only:
// workers don't run a dashboard, so on a worker this is skipped entirely
// rather than spending a timeout to fail. An explicit DashboardURL
// overrides that, for setups running the dashboard somewhere unusual.
func (d *Detector) fillClusterState(ctx context.Context, info *Info) {
	url := d.cfg.DashboardURL
	if url == "" {
		if info.Role != "head" {
			// Not an error: the backend gets the count by grouping the
			// fleet on gcs_address, which is better than any single
			// worker's view.
			info.Error = "alive_nodes unavailable on a worker (no local dashboard); group the fleet by gcs_address instead"
			return
		}
		// Always localhost, never NodeIP. We only ever query the head's OWN
		// dashboard, and Ray binds it to 127.0.0.1 unless the operator passes
		// --dashboard-host=0.0.0.0 — which includes 127.0.0.1 anyway. So
		// localhost is never worse and is often the only thing that answers.
		//
		// Using NodeIP here was actively harmful: on a node whose registered
		// Ray IP isn't locally routable, every refresh burned the full
		// timeout on both endpoints before giving up. Operators with a
		// genuinely remote dashboard set ray.dashboard_url.
		url = "http://127.0.0.1:" + defaultDashboardPort
	}
	info.DashboardURL = url

	n, err := fetchAliveNodes(ctx, url)
	if err != nil {
		// Ray's dashboard API is an internal surface that has changed shape
		// across releases, so a failure here is expected-ish and must not
		// cost us the block.
		info.Error = "dashboard query failed: " + err.Error()
		return
	}
	info.AliveNodes = &n
	info.Source = "cmdline+dashboard"
	info.Error = ""
}

// roleFor maps which daemons are present onto a role. gcs_server is the
// discriminator: exactly one node per cluster runs it.
func roleFor(rayletPID, gcsPID int) string {
	if gcsPID != 0 {
		return "head"
	}
	if rayletPID != 0 {
		return "worker"
	}
	return ""
}

// parseFlags pulls --key=value pairs out of a command line.
//
// Keys are stored verbatim (leading dashes stripped) rather than
// normalised, because Ray is inconsistent about `-` vs `_` and callers
// should be explicit about which spellings they accept — silently folding
// them would hide a version difference worth seeing.
//
// Only the --key=value form is handled. Ray's raylet uses it exclusively
// for the flags we read; space-separated values would need a per-flag arity
// table, which is more machinery than this warrants.
func parseFlags(cmdline string) map[string]string {
	out := map[string]string{}
	if cmdline == "" {
		return out
	}
	for _, tok := range strings.Fields(cmdline) {
		if !strings.HasPrefix(tok, "-") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimLeft(tok, "-"), "=")
		if !ok || k == "" {
			continue
		}
		// First occurrence wins; Ray doesn't repeat these.
		if _, exists := out[k]; !exists {
			out[k] = v
		}
	}
	return out
}

// parseStaticResourceList parses Ray's --static_resource_list, a flat
// comma-separated alternation of name,value:
//
//	node:10.10.10.7,1.0,CPU,20,GPU,1,memory,67108864
//
// Note "node:10.10.10.7" is itself a resource name containing a colon, so
// the pairs cannot be found by splitting on anything but position. A
// trailing unpaired name is ignored rather than guessed at.
func parseStaticResourceList(s string) map[string]float64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make(map[string]float64, len(parts)/2)
	for i := 0; i+1 < len(parts); i += 2 {
		name := strings.TrimSpace(parts[i])
		if name == "" {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(parts[i+1]), 64)
		if err != nil {
			continue
		}
		out[name] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sessionDirFromFlags derives Ray's session directory from the raylet's
// socket paths, e.g.
//
//	--raylet_socket_name=/tmp/ray/session_<id>/sockets/raylet
//	                     └────── session dir ──────┘
//
// Reading it from the socket path rather than assuming /tmp/ray means a
// non-default --temp-dir is handled for free.
func sessionDirFromFlags(flags map[string]string) string {
	for _, k := range []string{"raylet_socket_name", "store_socket_name", "raylet-socket-name", "store-socket-name"} {
		p := flags[k]
		if p == "" {
			continue
		}
		// .../<session>/sockets/<name> -> .../<session>
		dir := filepath.Dir(filepath.Dir(p))
		if filepath.Base(dir) == "sockets" {
			dir = filepath.Dir(dir)
		}
		if strings.HasPrefix(filepath.Base(dir), "session") {
			return dir
		}
	}
	return ""
}

// sessionFromDisk resolves the session name from the filesystem, for Ray
// versions whose raylet omits --session-name. Prefers the directory we
// already derived; falls back to Ray's default session_latest symlink.
// Returns (sessionName, sessionDir).
func sessionFromDisk(knownDir string) (string, string) {
	if knownDir != "" {
		if base := filepath.Base(knownDir); strings.HasPrefix(base, "session") && base != "session_latest" {
			return base, knownDir
		}
	}
	const latest = "/tmp/ray/session_latest"
	target, err := os.Readlink(latest)
	if err != nil {
		return "", ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(latest), target)
	}
	base := filepath.Base(target)
	if !strings.HasPrefix(base, "session") {
		return "", ""
	}
	return base, target
}

// dashboardClient is package-level so tests can point it at a stub server
// without a network.
var dashboardClient = &http.Client{Timeout: dashboardTimeout}

// fetchAliveNodes asks Ray's dashboard how many nodes are alive.
//
// Ray's dashboard API is internal and has changed shape across releases, so
// this tries the endpoints in order and tolerates several response shapes
// rather than binding to one. It counts nodes reported ALIVE instead of
// trusting a summary total, since a total that silently includes dead nodes
// would inflate a TP group's apparent size.
func fetchAliveNodes(ctx context.Context, base string) (int, error) {
	base = strings.TrimRight(base, "/")
	var lastErr error
	for _, path := range []string{"/nodes?view=summary", "/api/cluster_status"} {
		n, err := fetchAliveFrom(ctx, base+path)
		if err == nil {
			return n, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func fetchAliveFrom(ctx context.Context, url string) (int, error) {
	rctx, cancel := context.WithTimeout(ctx, dashboardTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := dashboardClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, &statusError{code: resp.StatusCode}
	}
	// Cap the read: a dashboard on a large cluster can return a lot, and we
	// only need a node count.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, err
	}
	return countAliveNodes(body)
}

// countAliveNodes extracts a live node count from a dashboard payload.
// Split out from the HTTP call so the shape-tolerance is directly testable
// against captured responses.
func countAliveNodes(body []byte) (int, error) {
	var envelope struct {
		Data struct {
			// /nodes?view=summary
			Summary []struct {
				Raylet struct {
					State string `json:"state"`
				} `json:"raylet"`
				// Some versions report the state at the top level.
				State string `json:"state"`
			} `json:"summary"`
			// /api/cluster_status
			ClusterStatus struct {
				Data struct {
					ClusterStatus struct {
						ActiveNodes map[string]int `json:"active_nodes"`
					} `json:"clusterStatus"`
				} `json:"data"`
			} `json:"clusterStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, err
	}

	if n := len(envelope.Data.Summary); n > 0 {
		alive := 0
		for _, s := range envelope.Data.Summary {
			state := s.Raylet.State
			if state == "" {
				state = s.State
			}
			// Older payloads omit state entirely; a listed node with no
			// state is counted, since the summary view lists live nodes.
			if state == "" || strings.EqualFold(state, "ALIVE") {
				alive++
			}
		}
		if alive > 0 {
			return alive, nil
		}
	}

	if m := envelope.Data.ClusterStatus.Data.ClusterStatus.ActiveNodes; len(m) > 0 {
		total := 0
		for _, c := range m {
			total += c
		}
		if total > 0 {
			return total, nil
		}
	}

	return 0, errNoNodeCount
}

type statusError struct{ code int }

func (e *statusError) Error() string { return "dashboard returned HTTP " + strconv.Itoa(e.code) }

type noNodeCountError struct{}

func (noNodeCountError) Error() string {
	return "no recognisable node count in dashboard response"
}

var errNoNodeCount = noNodeCountError{}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
