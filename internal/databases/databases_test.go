package databases

import (
	"strings"
	"testing"
)

// TestKnownDatabasesAreUnique guards against accidental duplicate Name
// fields in the fingerprint list — a duplicate would let a single
// process surface twice under different canonical names.
func TestKnownDatabasesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, fp := range knownDatabases {
		if seen[fp.Name] {
			t.Errorf("duplicate Name in knownDatabases: %q", fp.Name)
		}
		seen[fp.Name] = true
	}
	if len(seen) < 20 {
		t.Errorf("expected at least 20 known databases per the spec, got %d", len(seen))
	}
}

// TestKnownDatabasesShape — every fingerprint must declare procName or
// cmdlineSubstr (otherwise matchDB can't ever fire for it).
func TestKnownDatabasesShape(t *testing.T) {
	for _, fp := range knownDatabases {
		if len(fp.procName) == 0 && len(fp.cmdlineSubstr) == 0 {
			t.Errorf("%q has no procName and no cmdlineSubstr — unmatchable", fp.Name)
		}
		if fp.defaultPort < 0 {
			t.Errorf("%q has negative defaultPort", fp.Name)
		}
	}
}

func TestReBinaryVersion(t *testing.T) {
	re := reBinaryVersion("postgres")
	cases := []struct {
		in, want string
	}{
		{"/usr/lib/postgresql/15/bin/postgres -D /var/lib/postgresql/15/main", "15"},
		{"postgres 16.2 (Ubuntu 16.2-1)", "16.2"},
		{"/opt/postgres-14.5/bin/postgres", "14.5"},
		{"redis-server *:6379", ""},
	}
	for _, tc := range cases {
		m := re.FindStringSubmatch(tc.in)
		got := ""
		if len(m) >= 2 {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("reBinaryVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCmdlineHead_Truncates(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := cmdlineHead(long, 80)
	if len(got) != 80 {
		t.Errorf("len = %d, want 80", len(got))
	}
}

func TestPortsToCSV(t *testing.T) {
	if got := PortsToCSV(nil); got != "" {
		t.Errorf("nil → %q, want empty", got)
	}
	if got := PortsToCSV([]int{5432, 5433}); got != "5432,5433" {
		t.Errorf("got %q", got)
	}
}

// Postgres spawns 8+ worker processes that all return process.Name() ==
// "postgres" but only the parent holds the listening socket. The
// dedupe step must collapse to one entry per cluster (the parent with
// ports), not nine.
func TestDedupe_PostgresWorkers(t *testing.T) {
	in := []Database{
		{Name: "postgres", PID: 1011, Ports: []int{55432}},
		{Name: "postgres", PID: 1012}, // worker
		{Name: "postgres", PID: 1013}, // worker
		{Name: "postgres", PID: 1014}, // worker
		{Name: "postgres", PID: 1015}, // worker
		{Name: "postgres", PID: 1016}, // worker
		{Name: "postgres", PID: 1018}, // worker
		{Name: "postgres", PID: 1019}, // worker
		{Name: "postgres", PID: 1020}, // worker
	}
	out := dedupeDatabases(in)
	if len(out) != 1 {
		t.Fatalf("expected 1 deduped entry, got %d: %+v", len(out), out)
	}
	if out[0].PID != 1011 || PortsToCSV(out[0].Ports) != "55432" {
		t.Errorf("expected parent pid=1011 ports=[55432], got %+v", out[0])
	}
}

// Two Postgres clusters running on different ports must surface as TWO
// entries — the dedupe must not collapse distinct listeners.
func TestDedupe_TwoClustersOnDifferentPorts(t *testing.T) {
	in := []Database{
		{Name: "postgres", PID: 1011, Ports: []int{5432}},
		{Name: "postgres", PID: 1012}, // worker of 1011
		{Name: "postgres", PID: 2011, Ports: []int{5433}},
		{Name: "postgres", PID: 2012}, // worker of 2011
	}
	out := dedupeDatabases(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries (two clusters), got %d: %+v", len(out), out)
	}
}

// Unix-socket-only DB (or socket-enumeration denied) must still surface
// — fall back to the lowest-PID candidate so the operator can see it.
func TestDedupe_NoPortsAtAll(t *testing.T) {
	in := []Database{
		{Name: "redis", PID: 5000},
		{Name: "redis", PID: 5001},
	}
	out := dedupeDatabases(in)
	if len(out) != 1 || out[0].PID != 5000 {
		t.Errorf("expected single entry with lowest pid (5000), got %+v", out)
	}
}

// Multiple distinct DBs running on the same box — each canonical Name
// gets its own group, no cross-contamination.
func TestDedupe_DifferentDatabasesCoexist(t *testing.T) {
	in := []Database{
		{Name: "postgres", PID: 100, Ports: []int{5432}},
		{Name: "redis", PID: 200, Ports: []int{6379}},
		{Name: "mongodb", PID: 300, Ports: []int{27017}},
	}
	out := dedupeDatabases(in)
	if len(out) != 3 {
		t.Errorf("expected 3 entries (one per DB), got %d: %+v", len(out), out)
	}
}
