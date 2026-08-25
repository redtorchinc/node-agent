//go:build linux

package service

import (
	"strings"
	"testing"
)

// The drop-in is the other half of a contract with systemctlArgv in
// internal/services/systemd_linux.go. sudo matches command specs
// argument-by-argument, so every spec here must equal the agent's argv
// exactly. Issue #28 was the two drifting apart: the agent sent
// `--no-pager --no-ask-password` ahead of the verb, no spec matched, and
// every /actions/service call died on `sudo: a password is required`.
//
// These tests can't reach across packages to compare against systemctlArgv
// directly (both identifiers are unexported, and the dependency would run
// installer -> runtime, backwards). They instead pin the property that
// matters: the specs stay flagless, and they cover exactly the verbs the
// agent elevates.

// specLines returns the command specs, one per line, with the sudoers
// line-continuations and leading whitespace stripped.
func specLines(t *testing.T) []string {
	t.Helper()

	body := strings.ReplaceAll(sudoersDropIn, "\\\n", " ")
	var rule string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "rt-agent ") {
			rule = line
			break
		}
	}
	if rule == "" {
		t.Fatal("no rt-agent rule found in the drop-in")
	}
	_, cmds, ok := strings.Cut(rule, "NOPASSWD:")
	if !ok {
		t.Fatalf("rule has no NOPASSWD: tag: %q", rule)
	}
	var out []string
	for _, s := range strings.Split(cmds, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func TestSudoersSpecsAreFlagless(t *testing.T) {
	for _, spec := range specLines(t) {
		fields := strings.Fields(spec)
		if len(fields) != 3 {
			t.Errorf("spec %q has %d fields, want exactly 3 (path, verb, unit-pattern)", spec, len(fields))
			continue
		}
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") {
				t.Errorf("spec %q carries flag %q; the agent's sudo argv is flagless, so this spec can never match", spec, f)
			}
		}
	}
}

func TestSudoersCoversExactlyTheMutatingVerbs(t *testing.T) {
	// status/show need no privilege and are no longer sudo'd, so granting
	// them here would be dead privilege — the state issue #28 started from.
	want := map[string]int{"start": 0, "stop": 0, "restart": 0}

	for _, spec := range specLines(t) {
		fields := strings.Fields(spec)
		if len(fields) < 2 {
			continue
		}
		verb := fields[1]
		if _, ok := want[verb]; !ok {
			t.Errorf("spec %q grants unexpected verb %q", spec, verb)
			continue
		}
		want[verb]++
	}
	for verb, n := range want {
		// One spec per systemctl path (/bin and /usr/bin).
		if n != 2 {
			t.Errorf("verb %q appears in %d specs, want 2 (one per systemctl path)", verb, n)
		}
	}
}

func TestSudoersSpecsUseAbsoluteSystemctlPaths(t *testing.T) {
	// Must match the closed candidate list in systemctlPath(): a relative or
	// unlisted path would let the agent invoke something sudo won't allow.
	allowed := map[string]bool{"/bin/systemctl": true, "/usr/bin/systemctl": true}

	for _, spec := range specLines(t) {
		path := strings.Fields(spec)[0]
		if !allowed[path] {
			t.Errorf("spec %q uses path %q, not one of /bin/systemctl or /usr/bin/systemctl", spec, path)
		}
	}
}

func TestSudoersUnitPatternStaysScopedToRtVllm(t *testing.T) {
	// The hardcoded rt-vllm-* pattern is the safety rail: a misconfigured
	// config.yaml allowlist must not be able to reach sshd or docker.
	const pattern = "rt-vllm-[a-zA-Z0-9_-]*.service"

	for _, spec := range specLines(t) {
		fields := strings.Fields(spec)
		if got := fields[len(fields)-1]; got != pattern {
			t.Errorf("spec %q targets %q, want %q", spec, got, pattern)
		}
	}
}
