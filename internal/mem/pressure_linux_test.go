//go:build linux

package mem

import (
	"strings"
	"testing"
)

const psiSample = `some avg10=0.12 avg60=0.07 avg300=0.02 total=12345678
full avg10=0.04 avg60=0.01 avg300=0.00 total=42
`

func TestParsePSI_Normal(t *testing.T) {
	p := parsePSI(strings.NewReader(psiSample))
	if p == nil {
		t.Fatal("expected PSI, got nil")
	}
	if p.SomeAvg10 != 0.12 || p.SomeAvg60 != 0.07 {
		t.Errorf("some avg10/60 = %.2f/%.2f, want 0.12/0.07", p.SomeAvg10, p.SomeAvg60)
	}
	if p.FullAvg10 != 0.04 || p.FullAvg60 != 0.01 {
		t.Errorf("full avg10/60 = %.2f/%.2f, want 0.04/0.01", p.FullAvg10, p.FullAvg60)
	}
	if p.Classification != "normal" {
		t.Errorf("classification = %q, want normal", p.Classification)
	}
}

func TestParsePSI_Some(t *testing.T) {
	p := parsePSI(strings.NewReader(`some avg10=5.20 avg60=2.10 avg300=0.50 total=12345
full avg10=0.30 avg60=0.10 avg300=0.00 total=42
`))
	if p.Classification != "some" {
		t.Errorf("classification = %q, want some (some.avg10 > 1.0)", p.Classification)
	}
	if p.SomeAvg10 != 5.20 {
		t.Errorf("some avg10 = %.2f, want 5.20", p.SomeAvg10)
	}
}

func TestParsePSI_Full(t *testing.T) {
	p := parsePSI(strings.NewReader(`some avg10=8.40 avg60=4.20 avg300=2.10 total=99
full avg10=2.50 avg60=1.20 avg300=0.40 total=33
`))
	if p.Classification != "full" {
		t.Errorf("classification = %q, want full (full.avg10 > 1.0)", p.Classification)
	}
}

func TestParseVmstatSwap_Basic(t *testing.T) {
	sample := `nr_free_pages 12345
pswpin 100200
pswpout 99887766
nr_inactive_anon 999
`
	in, out, ok := parseVmstatSwap(strings.NewReader(sample))
	if !ok {
		t.Fatal("expected ok=true")
	}
	if in != 100200 {
		t.Errorf("pswpin = %d, want 100200", in)
	}
	if out != 99887766 {
		t.Errorf("pswpout = %d, want 99887766", out)
	}
}

func TestParseVmstatSwap_MissingCounters(t *testing.T) {
	// /proc/vmstat without pswpin/pswpout (very old kernel or stripped build).
	sample := `nr_free_pages 12345
nr_inactive_anon 999
`
	_, _, ok := parseVmstatSwap(strings.NewReader(sample))
	if ok {
		t.Errorf("expected ok=false when neither counter is present")
	}
}

func TestParseVmstatSwap_OneOfTwo(t *testing.T) {
	// pswpin present but pswpout missing → ok=false (need both for the
	// rate calculation to be meaningful).
	sample := `pswpin 12345
nr_free_pages 999
`
	_, _, ok := parseVmstatSwap(strings.NewReader(sample))
	if ok {
		t.Errorf("expected ok=false when only one counter present")
	}
}
