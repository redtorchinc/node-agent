//go:build linux

package netown

import "testing"

func TestCapEffMask(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		mask uint64
		ok   bool
	}{
		{"root full mask", "000001ffffffffff", 0x1ffffffffff, true},
		{"unprivileged", "0000000000000000", 0, true},
		{"exactly the two caps", "0000000000080004", 1<<capSysPtrace | 1<<capDACReadSearch, true},
		{"garbage", "zzzz", 0, false},
	}
	for _, c := range cases {
		body := "CapEff:\t" + c.hex + "\n"
		mask, ok := capEffMask(body)
		if ok != c.ok || mask != c.mask {
			t.Errorf("%s: got mask=%#x ok=%v, want mask=%#x ok=%v", c.name, mask, ok, c.mask, c.ok)
		}
	}
	if _, ok := capEffMask("Name:\tfoo\n"); ok {
		t.Error("no CapEff line must report !ok")
	}
}

func TestMissingAttributionCaps(t *testing.T) {
	if got := missingAttributionCaps(1<<capSysPtrace | 1<<capDACReadSearch); len(got) != 0 {
		t.Errorf("both caps present, want none missing, got %v", got)
	}
	if got := missingAttributionCaps(0); len(got) != 2 {
		t.Errorf("empty mask, want both missing, got %v", got)
	}
	if got := missingAttributionCaps(1 << capSysPtrace); len(got) != 1 || got[0] != "CAP_DAC_READ_SEARCH" {
		t.Errorf("want only CAP_DAC_READ_SEARCH missing, got %v", got)
	}
}
