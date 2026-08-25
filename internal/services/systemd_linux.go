//go:build linux

package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redtorchinc/node-agent/internal/config"
)

// sudoersDropInPath mirrors the constant of the same name in
// internal/service. Duplicated rather than exported because this package is
// the runtime consumer and that one is the installer — the dependency would
// otherwise run backwards. Only used to name the file in an error message.
const sudoersDropInPath = "/etc/sudoers.d/rt-node-agent"

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
		// Elevation was refused rather than the action failing. Say so
		// plainly and name the file to check — the raw sudo text alone
		// ("a password is required") sends operators looking at the Bearer
		// token instead of the drop-in.
		if isSudoAuthFailure(out) {
			return Result{
					Unit:   unit,
					Action: string(action),
					TookMS: time.Since(start).Milliseconds(),
				}, fmt.Errorf("%w: sudo refused `systemctl %s %s`; check the NOPASSWD command specs in %s match the agent's argv exactly (sudo matches arguments positionally): %w",
					ErrSudoDenied, action, unit, sudoersDropInPath, err)
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

// systemctlPaths are the only binary paths the agent will ever invoke, and
// they are exactly the paths enumerated in the sudoers drop-in
// (internal/service/sudoers_linux.go). Resolving through $PATH instead
// could land on a path the drop-in does not cover, so the list is closed.
//
// Both entries exist because /bin is a symlink to /usr/bin on some distros
// and a real directory on others.
var systemctlPaths = []string{"/bin/systemctl", "/usr/bin/systemctl"}

// systemctlPath picks the first entry that exists on this host. Resolved
// once — Snapshot() calls into here per allowed unit on every /health.
var systemctlPath = sync.OnceValue(func() string {
	for _, p := range systemctlPaths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	// Neither present: fall through to the first so the exec fails with a
	// plain ENOENT rather than something more confusing.
	return systemctlPaths[0]
})

// readOnlySystemctlVerbs are subcommands that only query state. systemd
// serves these to any local user over the system bus, so they run without
// sudo — routing them through sudo would widen the sudoers surface for no
// gain.
var readOnlySystemctlVerbs = map[string]bool{
	"show":   true,
	"status": true,
}

// systemctlArgv returns the binary path + argv for a systemctl invocation.
// Agents started by systemd as user `rt-agent` cannot mutate system units
// without elevation; the install path adds a sudoers drop-in scoped to
// rt-vllm-*.service. Read-only verbs, and the already-root case (the
// bootstrap-during-install path), skip sudo entirely.
//
// CRITICAL — the sudo branch's argv MUST match the drop-in in
// internal/service/sudoers_linux.go argument-for-argument.
//
// sudo compares command specs positionally, so a single extra flag ahead of
// the verb means the NOPASSWD rule does not match at all: sudo falls
// through to requiring a password, `-n` turns that into a hard failure, and
// every /actions/service call breaks. That was issue #28 — the agent sent
//
//	sudo -n /bin/systemctl --no-pager --no-ask-password start <unit>
//
// against a drop-in that allowed only
//
//	/bin/systemctl start <unit>
//
// so no service action worked on a default install. The two flags are also
// why the old `status` and `show` specs in the drop-in were dead code —
// `show` additionally appends --property=… after the unit, which no spec
// listed. Do not add flags to the sudo branch without adding them to the
// drop-in.
func systemctlArgv(args ...string) (string, []string) {
	bin := systemctlPath()

	// --no-pager: systemctl only pages on a TTY and we always capture into
	// a buffer, but be explicit. --no-ask-password: never block on a
	// credential prompt. Safe here because these paths bypass sudoers
	// matching; NOT safe in the sudo branch below.
	direct := func() (string, []string) {
		argv := make([]string, 0, len(args)+2)
		argv = append(argv, "--no-pager", "--no-ask-password")
		return bin, append(argv, args...)
	}
	if len(args) > 0 && readOnlySystemctlVerbs[args[0]] {
		return direct()
	}
	if isRoot() {
		return direct()
	}

	// Bare argv — see the sudoers-match note above. Note that `-n` is a
	// sudo option consumed by sudo itself, so it takes no part in command
	// spec matching.
	argv := make([]string, 0, len(args)+2)
	argv = append(argv, "-n", bin)
	return "/usr/bin/sudo", append(argv, args...)
}

// isSudoAuthFailure spots the "sudoers doesn't cover this argv" shape.
// Without this the operator gets a bare 500 carrying `sudo: a password is
// required` and has to work backwards to the drop-in themselves — which is
// exactly the debugging issue #28 describes.
//
// Each phrase below is already sudo-specific, so there is deliberately no
// broader "does this output mention sudo at all" pre-filter. An earlier
// version had one, and it rejected the single most important case: sudo's
// spec-mismatch message is
//
//	Sorry, user rt-agent is not allowed to execute '…' as root.
//
// which names neither "sudo" nor "password". The CI Linux job caught it.
func isSudoAuthFailure(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "a password is required") ||
		strings.Contains(s, "a terminal is required") ||
		strings.Contains(s, "no askpass program") ||
		strings.Contains(s, "not allowed to execute") ||
		strings.Contains(s, "is not in the sudoers file")
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
