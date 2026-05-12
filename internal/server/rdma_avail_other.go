//go:build !linux

package server

// rdmaAvailable is always false off Linux. RoCE/IB drivers exist on macOS
// and Windows in some forms, but rt-node-agent doesn't probe them — there
// is no installed base to support in the RedTorch fleet.
func rdmaAvailable() bool { return false }
