//go:build linux

package mem

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// probePSI reads /proc/pressure/memory. Available on kernels >= 4.20 with
// PSI enabled. Returns nil when the file is absent or unparseable rather
// than synthesizing values — silence beats a fabricated "all clear."
//
// File shape:
//
//	some avg10=0.10 avg60=0.05 avg300=0.01 total=12345
//	full avg10=0.00 avg60=0.00 avg300=0.00 total=42
func probePSI() *PSI {
	f, err := os.Open("/proc/pressure/memory")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parsePSI(f)
}

// parsePSI is the pure parser, exposed for tests.
func parsePSI(r io.Reader) *PSI {
	p := &PSI{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		row := fields[0]
		var avg10, avg60 float64
		for _, kv := range fields[1:] {
			switch {
			case strings.HasPrefix(kv, "avg10="):
				avg10, _ = strconv.ParseFloat(strings.TrimPrefix(kv, "avg10="), 64)
			case strings.HasPrefix(kv, "avg60="):
				avg60, _ = strconv.ParseFloat(strings.TrimPrefix(kv, "avg60="), 64)
			}
		}
		switch row {
		case "some":
			p.SomeAvg10 = avg10
			p.SomeAvg60 = avg60
		case "full":
			p.FullAvg10 = avg10
			p.FullAvg60 = avg60
		}
	}
	switch {
	case p.FullAvg10 > 1.0:
		p.Classification = "full"
	case p.SomeAvg10 > 1.0:
		p.Classification = "some"
	default:
		p.Classification = "normal"
	}
	return p
}

// probePressure is retained for legacy callers. New code reads probePSI()
// directly to get all four raw gauges plus the classification.
func probePressure() string {
	psi := probePSI()
	if psi == nil {
		return ""
	}
	return psi.Classification
}

// probeSwapCounters reads /proc/vmstat for pswpin / pswpout — the
// cumulative count of pages swapped in / out since boot. Returns
// (0,0,false) if /proc/vmstat is absent or neither counter is present.
func probeSwapCounters() (uint64, uint64, bool) {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	return parseVmstatSwap(f)
}

// parseVmstatSwap is the pure parser. /proc/vmstat is one "key value" per
// line; we only care about pswpin / pswpout.
func parseVmstatSwap(r io.Reader) (uint64, uint64, bool) {
	sc := bufio.NewScanner(r)
	var pin, pout uint64
	seenIn, seenOut := false, false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "pswpin":
			if n, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				pin = n
				seenIn = true
			}
		case "pswpout":
			if n, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				pout = n
				seenOut = true
			}
		}
	}
	return pin, pout, seenIn && seenOut
}

// probeHugePages reads /proc/meminfo for HugePages_Total/HugePages_Free.
// Returns (0,0,false) when neither is present (kernel without hugepage
// support, or zero pages reserved).
func probeHugePages() (int64, int64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	var total, free int64
	seen := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "HugePages_Total:":
			if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				total = n
				seen = true
			}
		case "HugePages_Free:":
			if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				free = n
				seen = true
			}
		}
	}
	return total, free, seen
}
