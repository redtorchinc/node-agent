//go:build linux

package mem

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// probePressure reads /proc/pressure/memory. Available on kernels >= 4.20
// with PSI enabled. Returns "" when the file is absent or unparseable
// rather than synthesizing a value.
func probePressure() string {
	f, err := os.Open("/proc/pressure/memory")
	if err != nil {
		return ""
	}
	defer f.Close()
	// Two lines: "some" and "full", each with avg10/avg60/avg300/total.
	// We classify "normal"/"some"/"full" by which avg10 is highest.
	var someAvg10, fullAvg10 float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		var avg10 float64
		for _, p := range parts[1:] {
			if strings.HasPrefix(p, "avg10=") {
				avg10, _ = strconv.ParseFloat(strings.TrimPrefix(p, "avg10="), 64)
				break
			}
		}
		switch parts[0] {
		case "some":
			someAvg10 = avg10
		case "full":
			fullAvg10 = avg10
		}
	}
	switch {
	case fullAvg10 > 1.0:
		return "full"
	case someAvg10 > 1.0:
		return "some"
	default:
		return "normal"
	}
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
