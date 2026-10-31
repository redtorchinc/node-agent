//go:build linux

package storage

import (
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestNFSVersionFromOpts(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"rw,vers=4.2,rsize=1048576,wsize=1048576,hard", "4.2"},
		{"rw,nfsvers=4.1,minorversion=1", "4.1"},
		{"rw,vers=3,rsize=32768", "3"},
		{"rw,actimeo=0", ""},
	}
	for _, tc := range cases {
		got := nfsVersionFromOpts(tc.in)
		if got != tc.want {
			t.Errorf("nfsVersionFromOpts(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitColonPath(t *testing.T) {
	// IPs in tests use RFC 5737 documentation ranges (TEST-NET-3 here) so
	// nothing in the repo looks like a real fleet address.
	srv, exp := splitColonPath("203.0.113.5:/srv/models")
	if srv != "203.0.113.5" || exp != "/srv/models" {
		t.Errorf("got (%q, %q)", srv, exp)
	}
	// IPv6 hostnames in /proc/self/mounts are kernel-rendered as bracketed
	// forms ("[2001:db8::1]:/export"); first-colon split is imperfect
	// there. Documented limitation; an operator-facing audit doesn't
	// depend on perfect host extraction.
}

func TestSplitCIFSPath(t *testing.T) {
	srv, share := splitCIFSPath("//server/models")
	if srv != "server" || share != "models" {
		t.Errorf("got (%q, %q)", srv, share)
	}
	srv2, share2 := splitCIFSPath(`\\fileserver\engineering`)
	if srv2 != "fileserver" || share2 != "engineering" {
		t.Errorf("got (%q, %q)", srv2, share2)
	}
}

func TestTrimOpts(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got := trimOpts(long)
	if len([]rune(got)) > 121 {
		t.Errorf("trimOpts didn't cap length: %d", len(got))
	}
	short := "rw,hard"
	if got := trimOpts(short); got != short {
		t.Errorf("trimOpts mangled short opts: %q", got)
	}
}

func TestFstypeToWire(t *testing.T) {
	if fstypeToWire("nfs") != "nfs" {
		t.Error("nfs should round-trip")
	}
	if fstypeToWire("nfs4") != "nfs4" {
		t.Error("nfs4 should round-trip")
	}
}

func withFakeStatfs(t *testing.T, fn func(string, *syscall.Statfs_t) error) {
	t.Helper()
	orig := statfsFn
	statfsFn = fn
	t.Cleanup(func() { statfsFn = orig })
}

// A network mount whose server never answers must not stall the probe, and
// repeated sweeps must not pile up goroutines behind it.
func TestStatfsBoundedHungMountIsStaleAndSingleFlight(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	withFakeStatfs(t, func(string, *syscall.Statfs_t) error {
		calls.Add(1)
		<-release
		return nil
	})

	start := time.Now()
	if _, stale, err := statfsBounded("/mnt/hung", 50*time.Millisecond); !stale || err != nil {
		t.Fatalf("first sweep: stale=%v err=%v, want stale", stale, err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("first sweep took %v, want about the 50ms deadline", el)
	}
	if _, stale, _ := statfsBounded("/mnt/hung", 50*time.Millisecond); !stale {
		t.Fatal("second sweep: want stale while the first call is still hung")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("statfs started %d times for one hung mount, want 1", n)
	}
}

func TestStatfsBoundedRecoversWhenServerAnswers(t *testing.T) {
	release := make(chan struct{})
	withFakeStatfs(t, func(_ string, st *syscall.Statfs_t) error {
		<-release
		st.Bsize, st.Blocks, st.Bfree = 4096, 1000, 250
		return nil
	})
	if _, stale, _ := statfsBounded("/mnt/slow", 20*time.Millisecond); !stale {
		t.Fatal("want stale while the server is hung")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, stale, err := statfsBounded("/mnt/slow", 200*time.Millisecond); !stale && err == nil && st.Blocks == 1000 {
			return
		}
	}
	t.Fatal("never returned fresh capacity after the server answered")
}

func TestBuildNFSEntryStaleOmitsCapacity(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	withFakeStatfs(t, func(string, *syscall.Statfs_t) error { <-release; return nil })
	orig := statfsTimeout
	statfsTimeout = 20 * time.Millisecond
	t.Cleanup(func() { statfsTimeout = orig })

	info := buildNFSEntry(mountEntry{device: "203.0.113.5:/srv/models", mountpoint: "/mnt/models-hung", fstype: "nfs4", opts: "rw,vers=4.2,soft"}, "4.2")
	if !info.Stale {
		t.Fatal("want Stale on a hung server")
	}
	if info.TotalGB != 0 || info.UsedGB != 0 || info.UsedPct != 0 {
		t.Fatalf("capacity must be omitted when stale, got %+v", info)
	}
	if info.Server != "203.0.113.5" || info.Export != "/srv/models" {
		t.Fatalf("identity fields still expected, got %+v", info)
	}
}

func TestBuildNFSEntryHealthyCapacity(t *testing.T) {
	withFakeStatfs(t, func(_ string, st *syscall.Statfs_t) error {
		st.Bsize, st.Blocks, st.Bfree = 1<<20, 2048, 512 // 2 GiB total, 1.5 GiB used
		return nil
	})
	info := buildNFSEntry(mountEntry{device: "203.0.113.5:/srv/models", mountpoint: "/mnt/models-ok", fstype: "nfs4", opts: "rw"}, "4.2")
	if info.Stale || info.TotalGB != 2 || info.UsedGB != 1.5 || info.UsedPct != 75 {
		t.Fatalf("got %+v, want 2 GiB / 1.5 GiB / 75%% and not stale", info)
	}
}
