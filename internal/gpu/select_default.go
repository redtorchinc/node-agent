//go:build !(darwin && arm64)

package gpu

func selectPlatform() (Probe, bool) { return nil, false }
