package gpu

// Select returns the Probe appropriate for the current host. On darwin/arm64
// the Apple Silicon probe is preferred; on any OS where nvidia-smi is on PATH
// the NvidiaSMI probe is used; otherwise Noop.
func Select() Probe {
	if p, ok := selectPlatform(); ok {
		return p
	}
	n := NewNvidiaSMI()
	if n.Available() {
		return n
	}
	return NewNoop()
}
