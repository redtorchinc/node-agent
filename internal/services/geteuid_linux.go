//go:build linux

package services

import "os"

// geteuid is a tiny wrapper used by the systemd manager so the Linux build
// can ask "are we root?" without dragging an os import into the manager
// interface file (kept platform-neutral).
func geteuid() int { return os.Geteuid() }
