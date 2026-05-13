package mem

// SwapProcess is one entry in /health.top_swap_processes[]. Linux-only —
// the wire shape is stable across platforms; other platforms return an
// empty slice so the field is always present in the payload.
type SwapProcess struct {
	PID         int    `json:"pid"`
	Name        string `json:"name"`
	SwapMB      int64  `json:"swap_mb"`
	CmdlineHead string `json:"cmdline_head,omitempty"`
}

// TopSwapProcesses returns the top n processes ranked by VmSwap, descending.
// On non-Linux platforms returns nil (no per-process swap accounting).
//
// The current implementation walks /proc/[pid]/status, which scales with
// process count but is bounded by the on-host /health cadence (and the
// outer 2s context). With n <= 10 we don't bother with a heap — a simple
// sort over all candidates is fine and clearer.
func TopSwapProcesses(n int) []SwapProcess { return topSwapProcesses(n) }
