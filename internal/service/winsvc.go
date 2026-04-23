//go:build windows

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const svcName = "rt-node-agent"

// install registers the service with the SCM and starts it. Requires Administrator.
func install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w (run from elevated PowerShell)", err)
	}
	defer m.Disconnect()

	// Fail clean if already installed.
	if s, err := m.OpenService(svcName); err == nil {
		s.Close()
		return fmt.Errorf("service %q already installed; run uninstall first", svcName)
	}

	s, err := m.CreateService(svcName, exe, mgr.Config{
		DisplayName:      "RedTorch Node Agent",
		Description:      "Load visibility and unload-on-demand for RedTorch dispatcher",
		StartType:        mgr.StartAutomatic,
	}, "run")
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	fmt.Println("rt-node-agent installed and started (Windows Service)")
	return nil
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		// Treat "not installed" as success.
		return nil
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	fmt.Println("rt-node-agent uninstalled")
	return nil
}

func startSvc() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Start()
}

func stopSvc() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return err
	}
	defer s.Close()
	_, err = s.Control(svc.Stop)
	return err
}

func status() (State, error) {
	m, err := mgr.Connect()
	if err != nil {
		return StateUnknown, err
	}
	defer m.Disconnect()
	s, err := m.OpenService(svcName)
	if err != nil {
		return StateNotInstalled, nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return StateUnknown, nil
	}
	switch st.State {
	case svc.Running, svc.StartPending:
		return StateRunning, nil
	case svc.Stopped, svc.StopPending:
		return StateStopped, nil
	}
	return StateUnknown, nil
}

// runner is the svc.Handler used by the SCM to drive our lifecycle.
type runner struct {
	ctx    context.Context
	cancel context.CancelFunc
	runFn  func(context.Context) error
}

func (r *runner) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (ssec bool, errno uint32) {
	const accept = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	// Kick off the server in a goroutine so we can respond to SCM control requests.
	done := make(chan error, 1)
	go func() { done <- r.runFn(r.ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: accept}

loop:
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				r.cancel()
				break loop
			}
		case err := <-done:
			if err != nil {
				return true, 1
			}
			break loop
		}
	}
	// Drain.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	status <- svc.Status{State: svc.Stopped}
	return false, 0
}

// RunIfWindowsService returns (true, err) if the current process was started
// by the SCM and has been driven to completion. In that case main.go should
// return without doing anything else. When invoked from a console the call
// is a no-op.
//
// The srv and reporter parameters are typed as `any` because the real types
// live in other packages; this function bridges them to a plain runFn using
// a small unexported interface visible only on Windows.
func RunIfWindowsService(ctx context.Context, srv any, reporter any) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, nil
	}
	s, ok := srv.(interface {
		Run(context.Context) error
	})
	if !ok {
		return true, fmt.Errorf("winsvc: srv has no Run(context.Context) error")
	}
	_ = reporter // currently unused; reserved for future SCM-side status pushes

	childCtx, cancel := context.WithCancel(ctx)
	r := &runner{ctx: childCtx, cancel: cancel, runFn: s.Run}
	return true, svc.Run(svcName, r)
}
