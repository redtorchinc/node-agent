//go:build darwin && arm64

package gpu

// On Apple Silicon, prefer the native probe over nvidia-smi (which won't be
// present). Intel Macs with an eGPU fall through to the generic nvidia-smi
// path in Select().
func selectPlatform() (Probe, bool) { return NewAppleSilicon(), true }
