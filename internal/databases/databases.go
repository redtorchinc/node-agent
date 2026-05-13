// Package databases auto-detects running database servers on the node.
//
// Detection is process-based: enumerate processes via gopsutil and match
// against a fixed list of well-known DB binaries (Postgres, MySQL,
// MongoDB, Redis, vector stores, graph DBs, …). For each match we surface
// what we can read without credentials: PID, process name, listening
// ports, RSS, CPU%, command-line head, and a best-effort version string
// parsed out of the command line.
//
// No config. The dispatcher uses this purely as a fingerprint — knowing a
// node runs Postgres is informational; deciding whether to dispatch there
// is the backend's call. Cheap on every /health (single process scan
// shared with the swap-process probe in practice).
package databases

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

func sortDatabases(s []Database) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Name != s[j].Name {
			return s[i].Name < s[j].Name
		}
		return s[i].PID < s[j].PID
	})
}

// cacheTTL outlasts the case-manager's 30s response cache so backend
// polls always hit warm. DB presence doesn't flip second-to-second, and
// net.Connections on macOS shells out to lsof which spikes to 5-10s
// under load — keeping the result longer is correct. The keep-warm
// ticker in internal/health/StartBackground refreshes well under this
// TTL so idle agents stay warm.
const cacheTTL = 30 * time.Second

var (
	cacheMu   sync.Mutex
	cached    []Database
	cachedAt  time.Time
)

// Database is one entry in /health.databases[].
type Database struct {
	// Name is the canonical short label ("postgres", "mysql", …) — stable
	// for the dispatcher to switch on. See knownDatabases for the full set.
	Name string `json:"name"`

	// PID, ProcessName, CmdlineHead identify the running process. RSS and
	// CPUPct are point-in-time. Uptime is derived from process create time.
	PID         int     `json:"pid"`
	ProcessName string  `json:"process_name,omitempty"`
	CmdlineHead string  `json:"cmdline_head,omitempty"`
	RSSMB       int64   `json:"rss_mb,omitempty"`
	CPUPct      float64 `json:"cpu_pct,omitempty"`
	UptimeS     int64   `json:"uptime_s,omitempty"`

	// Ports are TCP ports the process is listening on. Empty when none are
	// open or the agent lacks permission to enumerate sockets for the PID.
	Ports []int `json:"ports,omitempty"`

	// Version is a best-effort parse of the command-line or process name.
	// Empty when no version can be extracted without shelling out.
	Version string `json:"version,omitempty"`
}

// knownDatabases is the fingerprint list. Order is the canonical output
// order; the matcher returns the first hit. Names are matched against
// process Name() (binary name) primarily, with cmdline fallback for
// java-wrapped DBs (Cassandra, Elasticsearch, Neo4j) and python-wrapped
// vector stores (ChromaDB).
var knownDatabases = []dbFingerprint{
	{Name: "postgres", procName: []string{"postgres", "postgresql"}, defaultPort: 5432, versionRE: reBinaryVersion(`postgres`)},
	{Name: "mysql", procName: []string{"mysqld"}, defaultPort: 3306},
	{Name: "mariadb", procName: []string{"mariadbd"}, defaultPort: 3306},
	{Name: "mongodb", procName: []string{"mongod"}, defaultPort: 27017},
	{Name: "redis", procName: []string{"redis-server"}, defaultPort: 6379, versionRE: regexp.MustCompile(`v=(\S+)`)},
	{Name: "memcached", procName: []string{"memcached"}, defaultPort: 11211},
	{Name: "cassandra", procName: []string{"cassandra"}, cmdlineSubstr: []string{"org.apache.cassandra.service.CassandraDaemon"}, defaultPort: 9042},
	{Name: "scylla", procName: []string{"scylla"}, defaultPort: 9042},
	{Name: "elasticsearch", procName: []string{"elasticsearch"}, cmdlineSubstr: []string{"org.elasticsearch.bootstrap.Elasticsearch"}, defaultPort: 9200},
	{Name: "opensearch", procName: []string{"opensearch"}, cmdlineSubstr: []string{"org.opensearch.bootstrap.OpenSearch"}, defaultPort: 9200},
	{Name: "neo4j", procName: []string{"neo4j"}, cmdlineSubstr: []string{"org.neo4j.server.startup", "neo4j-server"}, defaultPort: 7687},
	{Name: "influxdb", procName: []string{"influxd"}, defaultPort: 8086},
	{Name: "clickhouse", procName: []string{"clickhouse-server", "clickhouse"}, cmdlineSubstr: []string{"clickhouse-server"}, defaultPort: 9000},
	{Name: "cockroachdb", procName: []string{"cockroach"}, defaultPort: 26257},
	{Name: "etcd", procName: []string{"etcd"}, defaultPort: 2379},
	{Name: "qdrant", procName: []string{"qdrant"}, defaultPort: 6333},
	{Name: "weaviate", procName: []string{"weaviate"}, defaultPort: 8080},
	{Name: "milvus", procName: []string{"milvus"}, cmdlineSubstr: []string{"milvus run"}, defaultPort: 19530},
	{Name: "chromadb", procName: []string{"chroma"}, cmdlineSubstr: []string{"chromadb.app", "chroma run", "chroma_server"}, defaultPort: 8000},
	{Name: "dragonflydb", procName: []string{"dragonfly"}, defaultPort: 6379},
}

type dbFingerprint struct {
	Name          string
	procName      []string
	cmdlineSubstr []string
	defaultPort   int
	versionRE     *regexp.Regexp
}

// Probe enumerates running DB servers. The result is cached for cacheTTL
// (5s) so the /health hot path doesn't pay for a fresh process scan +
// socket enumeration every call. macOS's gopsutil/net.Connections shells
// out to lsof and can spike past the case-manager's 2s client deadline
// on a cold call; caching pushes that cost off the request path.
//
// Respects ctx for the socket-enumeration step only — once we have the
// process list, walking it is fast (< 200ms for a few hundred procs on
// macOS/Linux) and not worth interrupting mid-loop. A truncated list
// would surface a flickering /health (DB appears / disappears between
// calls) which is worse than a slightly-late one.
func Probe(ctx context.Context) []Database {
	cacheMu.Lock()
	if !cachedAt.IsZero() && time.Since(cachedAt) < cacheTTL {
		out := append([]Database(nil), cached...)
		cacheMu.Unlock()
		return out
	}
	cacheMu.Unlock()

	procs, err := process.Processes()
	if err != nil {
		return []Database{}
	}

	// Build a PID-keyed listening-ports index up front. gopsutil/net's
	// Connections() walks /proc/net/tcp once; far cheaper than calling it
	// per-process. On macOS/Windows it uses lsof / GetExtendedTcpTable.
	listening := buildListenIndex(ctx)
	// After the socket scan, ignore ctx for the rest of the probe — see
	// docstring rationale. Capping the actual cost is the caller's job
	// via overall /health budget; we just don't want to surface an
	// inconsistent "DB present but no rows" intermediate state.

	// Two-pass: first collect every candidate, then dedupe. Most DBs spawn
	// worker children that inherit the parent's process name (Postgres has
	// 8+ workers per cluster; MySQL's InnoDB threads share the name). We
	// only emit the process holding the listening port — workers never do,
	// so this collapses naturally to one entry per actual server.
	candidates := make([]Database, 0)
	for _, p := range procs {
		pid := int(p.Pid)
		name, _ := p.Name()
		fp := matchDB(name, p)
		if fp == nil {
			continue
		}
		cmdline, _ := p.Cmdline()
		rss := int64(0)
		if mi, err := p.MemoryInfo(); err == nil && mi != nil {
			rss = int64(mi.RSS / 1024 / 1024)
		}
		cpu, _ := p.CPUPercent()
		candidates = append(candidates, Database{
			Name:        fp.Name,
			PID:         pid,
			ProcessName: name,
			CmdlineHead: cmdlineHead(cmdline, 200),
			RSSMB:       rss,
			CPUPct:      round2(cpu),
			UptimeS:     uptimeSeconds(p),
			Ports:       listening[pid],
			Version:     extractVersion(fp, cmdline),
		})
	}
	result := dedupeDatabases(candidates)
	cacheMu.Lock()
	cached = result
	cachedAt = time.Now()
	cacheMu.Unlock()
	return result
}

// ResetCache clears the in-memory probe cache. Tests use this to force a
// fresh scan; production code never calls it.
func ResetCache() {
	cacheMu.Lock()
	cached = nil
	cachedAt = time.Time{}
	cacheMu.Unlock()
}

// Refresh forces a fresh probe and updates the cache, regardless of
// current cache age. Used by the keep-warm ticker in
// internal/health/StartBackground — calling Probe() during the cache's
// fresh window would no-op, leaving the next /health stranded on a
// cold cache once the TTL expired.
func Refresh(ctx context.Context) {
	ResetCache()
	_ = Probe(ctx)
}

// dedupeDatabases collapses worker processes into one entry per server
// instance. Rule: prefer the candidate with a non-empty Ports list (that's
// the parent / listener). If multiple candidates of the same canonical
// Name share an identical Ports tuple, keep the lowest-PID one (the
// parent — children always have higher PIDs). Different ports under the
// same Name (e.g. two Postgres clusters on 5432 and 5433) get separate
// entries by design.
//
// If a candidate has no Ports at all and another candidate of the same
// Name does have Ports, the no-port one is dropped — it's a worker, not
// a separate server. If no candidate of a given Name has ports (DB only
// listens on a unix socket, or socket enumeration was denied), keep the
// lowest-PID one so the DB is still visible.
func dedupeDatabases(in []Database) []Database {
	// Group candidates by canonical Name.
	byName := map[string][]Database{}
	for _, d := range in {
		byName[d.Name] = append(byName[d.Name], d)
	}
	out := make([]Database, 0, len(byName))
	for _, group := range byName {
		// Split into "has ports" / "no ports".
		var withPorts, withoutPorts []Database
		for _, d := range group {
			if len(d.Ports) > 0 {
				withPorts = append(withPorts, d)
			} else {
				withoutPorts = append(withoutPorts, d)
			}
		}
		if len(withPorts) > 0 {
			// Dedupe by sorted-ports tuple. Lowest PID per tuple wins.
			byPorts := map[string]Database{}
			for _, d := range withPorts {
				key := PortsToCSV(d.Ports)
				if existing, ok := byPorts[key]; !ok || d.PID < existing.PID {
					byPorts[key] = d
				}
			}
			for _, d := range byPorts {
				out = append(out, d)
			}
			continue
		}
		// No candidate has ports — keep the lowest-PID one so the DB is
		// still surfaced (unix-socket-only, or socket enumeration denied).
		best := withoutPorts[0]
		for _, d := range withoutPorts[1:] {
			if d.PID < best.PID {
				best = d
			}
		}
		out = append(out, best)
	}
	// Stable output order: sort by Name, then PID.
	sortDatabases(out)
	return out
}

// matchDB returns the fingerprint for a process or nil. Match priority:
//  1. Exact process-name match (cheapest, most reliable).
//  2. Executable basename match — covers darwin's 16-char comm truncation
//     ("/Applications/Po" for a postgres at /Applications/Postgres.app/…)
//     and Linux processes whose comm has been overwritten via prctl
//     (Postgres worker procs rename themselves "postgres: io worker 0").
//  3. cmdline substring match — last resort for java-wrapped DBs whose
//     process name is just "java" but cmdline contains the app class.
func matchDB(procName string, p *process.Process) *dbFingerprint {
	low := strings.ToLower(procName)
	for i := range knownDatabases {
		for _, n := range knownDatabases[i].procName {
			if low == n {
				return &knownDatabases[i]
			}
		}
	}
	// (2) Executable basename. Process.Exe() returns full path; basename
	// strips the directory.
	if exe, err := p.Exe(); err == nil && exe != "" {
		base := strings.ToLower(filepath.Base(exe))
		// Strip Windows ".exe" suffix so "postgres.exe" matches "postgres".
		base = strings.TrimSuffix(base, ".exe")
		for i := range knownDatabases {
			for _, n := range knownDatabases[i].procName {
				if base == n {
					return &knownDatabases[i]
				}
			}
		}
	}
	// (3) Cmdline substring (only for fingerprints that declare one).
	cmdline, err := p.Cmdline()
	if err != nil || cmdline == "" {
		return nil
	}
	lc := strings.ToLower(cmdline)
	for i := range knownDatabases {
		for _, sub := range knownDatabases[i].cmdlineSubstr {
			if strings.Contains(lc, strings.ToLower(sub)) {
				return &knownDatabases[i]
			}
		}
	}
	return nil
}

// buildListenIndex returns pid -> sorted unique listening TCP ports. Uses
// gopsutil/net.Connections("tcp"). On macOS this shells out to lsof and
// can take 3-5s under load; we give it its own 5s budget independent of
// the caller's ctx so the result is populated for the first cache miss.
// Subsequent /health calls within cacheTTL get cached results instantly.
//
// When the OS denies enumeration of sockets for processes we don't own
// (typical when the agent runs as a regular user but a DB runs as root),
// the corresponding PIDs are simply absent and the Database entry has
// Ports=nil — truthful absence.
func buildListenIndex(parent context.Context) map[int][]int {
	const socketEnumBudget = 5 * time.Second
	deadline, ok := parent.Deadline()
	var ctx context.Context
	var cancel context.CancelFunc
	if !ok || time.Until(deadline) < socketEnumBudget {
		// Caller's ctx is tighter than the socket enum needs. Detach and
		// use our own budget so the cache miss gets fresh ports.
		ctx, cancel = context.WithTimeout(context.Background(), socketEnumBudget)
	} else {
		ctx, cancel = context.WithCancel(parent)
	}
	defer cancel()

	index := map[int][]int{}
	conns, err := net.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		// Some platforms return EPERM at this level; v6/v4 split also varies.
		// Try "all" as a last resort.
		conns2, err2 := net.ConnectionsWithContext(ctx, "all")
		if err2 != nil {
			return index
		}
		conns = conns2
	}
	for _, c := range conns {
		if c.Status != "LISTEN" {
			continue
		}
		if c.Pid <= 0 {
			continue
		}
		port := int(c.Laddr.Port)
		if port == 0 {
			continue
		}
		pid := int(c.Pid)
		// Dedup while preserving order.
		exists := false
		for _, p := range index[pid] {
			if p == port {
				exists = true
				break
			}
		}
		if !exists {
			index[pid] = append(index[pid], port)
		}
	}
	return index
}

func uptimeSeconds(p *process.Process) int64 {
	ms, err := p.CreateTime()
	if err != nil || ms == 0 {
		return 0
	}
	created := time.UnixMilli(ms)
	d := time.Since(created)
	if d < 0 {
		return 0
	}
	return int64(d.Seconds())
}

func extractVersion(fp *dbFingerprint, cmdline string) string {
	if fp.versionRE == nil || cmdline == "" {
		return ""
	}
	m := fp.versionRE.FindStringSubmatch(cmdline)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// reBinaryVersion matches things like "postgres 15.4" or
// "/usr/lib/postgresql/15/bin/postgres". For now we only extract version
// when the cmdline embeds it directly — shelling out for `--version` is
// too expensive on every /health call.
func reBinaryVersion(binary string) *regexp.Regexp {
	// Matches e.g. "postgresql/15/" → "15".
	return regexp.MustCompile(`(?i)` + regexp.QuoteMeta(binary) + `(?:ql)?[/ -]+(\d+(?:\.\d+)*)`)
}

func cmdlineHead(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// PortsToCSV is a small helper exposed for tests / debugging — turns the
// Ports slice into a stable comma-separated string.
func PortsToCSV(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p))
	}
	return strings.Join(parts, ",")
}
