//go:build linux

package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
)

// systemdManager invokes systemctl via sudo (the install path drops a
// sudoers fragment that restricts the agent user to rt-vllm-*.service
// patterns — see internal/service/sudoers_linux.go).
type systemdManager struct {
	cfg config.ServicesConfig
}

func newManager(cfg config.ServicesConfig) Manager {
	return &systemdManager{cfg: cfg}
}

func (m *systemdManager) Capabilities() []config.ServiceAllowedEntry { return m.cfg.Allowed }

func (m *systemdManager) Do(ctx context.Context, unit string, action Action) (Result, error) {
	if _, err := validate(m.cfg, unit, action); err != nil {
		return Result{}, err
	}
	start := time.Now()

	// Pass unit as a discrete argv element. exec.Cmd does not invoke a
	// shell — injection via a crafted unit name is structurally impossible.
	cmd, args := systemctlArgv(string(action), unit)
	out, err := runWithTimeout(ctx, 10*time.Second, cmd, args...)
	if err != nil {
		// Heuristic: systemctl's "unit not found" message is stable enough
		// to map to a typed error so the HTTP layer can return 404 instead
		// of 500.
		if isUnitNotFound(out, err) {
			return Result{}, fmt.Errorf("%w: %s", ErrUnitNotFound, unit)
		}
		return Result{
			Unit:   unit,
			Action: string(action),
			TookMS: time.Since(start).Milliseconds(),
		}, err
	}

	res := Result{
		Unit:   unit,
		Action: string(action),
		TookMS: time.Since(start).Milliseconds(),
	}
	// After any mutating action also fetch state — handy for the dispatcher
	// which would otherwise need a second call.
	st, _ := m.showState(ctx, unit)
	res.ActiveState = st.ActiveState
	res.SubState = st.SubState
	res.MainPID = st.MainPID
	res.MemoryMB = st.MemoryMB
	return res, nil
}

func (m *systemdManager) Snapshot(ctx context.Context) []State {
	out := make([]State, 0, len(m.cfg.Allowed))
	for _, e := range m.cfg.Allowed {
		st, _ := m.showState(ctx, e.Name)
		st.Unit = e.Name
		out = append(out, st)
	}
	return out
}

// showState shells `systemctl show <unit> --property=...` and parses the
// property=value output. Quiet (errors absorbed) — we'd rather under-report
// than fail /health on a transient systemd hiccup.
func (m *systemdManager) showState(ctx context.Context, unit string) (State, error) {
	cmd, args := systemctlArgv("show", unit, "--property=ActiveState,SubState,MainPID,MemoryCurrent")
	out, err := runWithTimeout(ctx, 3*time.Second, cmd, args...)
	if err != nil {
		return State{Unit: unit}, err
	}
	st := State{Unit: unit}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := splitProp(line)
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.ActiveState = v
		case "SubState":
			st.SubState = v
		case "MainPID":
			if n, err := strconv.Atoi(v); err == nil {
				st.MainPID = n
			}
		case "MemoryCurrent":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				st.MemoryMB = n / 1024 / 1024
			}
		}
	}
	return st, nil
}

// systemctlArgv returns the cmd path + argv for either a direct call or a
// sudo'd one. Agents started by systemd as user `rt-agent` cannot control
// system units without elevation; the install path adds a sudoers drop-in
// scoped to rt-vllm-*.service.
//
// When the agent is already root (the bootstrap-during-install path),
// running systemctl directly is fine.
func systemctlArgv(args ...string) (string, []string) {
	// Note: never use shell expansion — args is passed as a slice to
	// exec.Cmd which doesn't shell out.
	if isRoot() {
		return "/bin/systemctl", append([]string{"--no-pager", "--no-ask-password"}, args...)
	}
	return "/usr/bin/sudo", append([]string{
		"-n",                          // never prompt
		"/bin/systemctl",
		"--no-pager", "--no-ask-password",
	}, args...)
}

func isRoot() bool {
	// Cheap: read /proc/self/status… or just stat /etc/shadow. For our
	// purposes os.Geteuid is fine.
	return geteuid() == 0
}

func runWithTimeout(ctx context.Context, d time.Duration, name string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		combined := stderr.Bytes()
		if len(combined) == 0 {
			combined = stdout.Bytes()
		}
		return combined, fmt.Errorf("%s: %w", name, errors.Join(err, errors.New(strings.TrimSpace(string(combined)))))
	}
	return stdout.Bytes(), nil
}

func isUnitNotFound(out []byte, _ error) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "not loaded") ||
		strings.Contains(s, "could not be found") ||
		strings.Contains(s, "no such file") ||
		strings.Contains(s, "unit") && strings.Contains(s, "not found")
}

func splitProp(line string) (string, string, bool) {
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	return line[:i], line[i+1:], true
}
