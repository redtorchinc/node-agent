// Package service installs and manages rt-node-agent as a native OS service:
// systemd on Linux, launchd on macOS, and a Windows Service via the SCM.
//
// Each platform implementation lives behind a //go:build tag. The public
// surface is Install/Uninstall/Start/Stop/PrintStatus and the Windows-only
// RunIfWindowsService helper invoked from main.go.
package service

import "fmt"

// State is the coarse-grained service state used by PrintStatus.
type State int

const (
	StateUnknown State = iota
	StateNotInstalled
	StateStopped
	StateRunning
)

func (s State) String() string {
	switch s {
	case StateNotInstalled:
		return "not-installed"
	case StateStopped:
		return "stopped"
	case StateRunning:
		return "running"
	default:
		return "unknown"
	}
}

// PrintStatus writes a one-line status to stdout.
func PrintStatus() error {
	st, err := status()
	if err != nil {
		return err
	}
	fmt.Println("rt-node-agent: " + st.String())
	return nil
}

// Install registers the service with the platform service manager and starts
// it. Requires appropriate privileges (root on Linux/macOS, Administrator on
// Windows); returns a descriptive error otherwise.
func Install() error { return install() }

// Uninstall stops and removes the service. Safe to call if not installed.
func Uninstall() error { return uninstall() }

// Start starts an already-installed service.
func Start() error { return startSvc() }

// Stop stops a running service.
func Stop() error { return stopSvc() }
