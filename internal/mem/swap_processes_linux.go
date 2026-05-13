//go:build linux

package mem

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// topSwapProcesses walks /proc/[pid]/status. The relevant lines:
//
//	Name:	postgres
//	VmSwap:	   1234 kB
//
// We ignore processes with VmSwap == 0. Cmdline comes from
// /proc/[pid]/cmdline (NUL-separated argv); we surface the first 80 chars.
func topSwapProcesses(n int) []SwapProcess {
	if n <= 0 {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var candidates []SwapProcess
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		swap, name, ok := readSwapAndName(pid)
		if !ok || swap == 0 {
			continue
		}
		candidates = append(candidates, SwapProcess{
			PID:         pid,
			Name:        name,
			SwapMB:      swap,
			CmdlineHead: readCmdlineHead(pid, 80),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].SwapMB > candidates[j].SwapMB
	})
	if len(candidates) > n {
		candidates = candidates[:n]
	}
	return candidates
}

// readSwapAndName scans /proc/<pid>/status. Returns (swap_mb, name, ok).
// ok=false if status is unreadable (process gone, perm denied).
func readSwapAndName(pid int) (int64, string, bool) {
	f, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, "", false
	}
	defer f.Close()
	var swapKB int64
	var name string
	sawSwap := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		case strings.HasPrefix(line, "VmSwap:"):
			// "VmSwap:    1234 kB"
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					swapKB = v
					sawSwap = true
				}
			}
		}
	}
	if !sawSwap {
		return 0, name, true // process exists but no VmSwap line — treat as 0
	}
	return swapKB / 1024, name, true
}

func readCmdlineHead(pid, max int) string {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	// cmdline is NUL-separated argv. Replace NULs with spaces, trim trailing.
	s := strings.TrimRight(strings.ReplaceAll(string(b), "\x00", " "), " ")
	if len(s) > max {
		return s[:max]
	}
	return s
}
