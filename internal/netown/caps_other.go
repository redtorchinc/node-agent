//go:build !linux

package netown

// attributionHint is Linux-only. launchd runs the agent as root and the
// Windows service runs as LocalSystem, so there is no capability gap to
// diagnose on those platforms.
func attributionHint() string { return "" }
