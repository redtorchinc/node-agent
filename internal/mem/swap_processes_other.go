//go:build !linux

package mem

// topSwapProcesses is Linux-only — macOS and Windows don't expose
// per-process swap usage cheaply. Returns nil so the composer emits an
// empty array.
func topSwapProcesses(n int) []SwapProcess { return nil }
