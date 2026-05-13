//go:build linux

package storage

import "testing"

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
	srv, exp := splitColonPath("10.0.0.5:/srv/models")
	if srv != "10.0.0.5" || exp != "/srv/models" {
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
