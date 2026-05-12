//go:build !linux

package service

// installSudoersDropIn is a no-op on non-Linux platforms. macOS launchd and
// Windows SCM don't have a sudoers analogue; service control on those
// platforms goes through launchctl/SCM with their own ACLs.
func installSudoersDropIn() error { return nil }

// removeSudoersDropIn is a no-op on non-Linux platforms.
func removeSudoersDropIn() error { return nil }
