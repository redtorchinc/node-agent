//go:build linux

package services

import (
	"strings"
	"testing"
)

// The sudo argv is half of a contract with the sudoers drop-in in
// internal/service/sudoers_linux.go, which grants flagless specs:
//
//	/bin/systemctl start rt-vllm-*.service
//
// sudo matches command specs argument-by-argument, so ANY flag inserted
// between the systemctl path and the verb makes the NOPASSWD rule miss.
// With `-n` that is a hard failure and every /actions/service call breaks —
// issue #28. These tests exist to make that regression loud.

func TestSystemctlArgvSudoBranchCarriesNoFlagsBeforeVerb(t *testing.T) {
	if isRoot() {
		t.Skip("sudo branch is unreachable as root")
	}
	for _, verb := range []string{"start", "stop", "restart"} {
		bin, argv := systemctlArgv(verb, "rt-vllm-llama3.service")

		if bin != "/usr/bin/sudo" {
			t.Fatalf("%s: bin = %q, want /usr/bin/sudo", verb, bin)
		}
		// -n must stay: it is a sudo option (not part of spec matching) and
		// turns a missed rule into an immediate error instead of a hang.
		want := []string{"-n", systemctlPath(), verb, "rt-vllm-llama3.service"}
		if len(argv) != len(want) {
			t.Fatalf("%s: argv = %q, want exactly %q", verb, argv, want)
		}
		for i := range want {
			if argv[i] != want[i] {
				t.Fatalf("%s: argv[%d] = %q, want %q (full: %q)", verb, i, argv[i], want[i], argv)
			}
		}
		// Belt and braces: the verb must sit immediately after the binary.
		for _, a := range argv[2:] {
			if strings.HasPrefix(a, "-") {
				t.Errorf("%s: flag %q in the sudo'd command; sudoers specs are flagless, so this will not match", verb, a)
			}
		}
	}
}

func TestSystemctlArgvReadOnlyVerbsSkipSudo(t *testing.T) {
	// show/status need no privilege. Keeping them off the sudo path is what
	// lets them carry --property=... and --no-pager, neither of which any
	// sudoers spec covers.
	for _, verb := range []string{"show", "status"} {
		bin, argv := systemctlArgv(verb, "rt-vllm-llama3.service")

		if bin == "/usr/bin/sudo" {
			t.Errorf("%s: routed through sudo; read-only verbs should call systemctl directly", verb)
		}
		if bin != systemctlPath() {
			t.Errorf("%s: bin = %q, want %q", verb, bin, systemctlPath())
		}
		if len(argv) == 0 || argv[0] != "--no-pager" {
			t.Errorf("%s: argv = %q, want --no-pager first", verb, argv)
		}
	}
}

func TestSystemctlArgvShowKeepsPropertyArg(t *testing.T) {
	const props = "--property=ActiveState,SubState,MainPID,MemoryCurrent"
	_, argv := systemctlArgv("show", "rt-vllm-llama3.service", props)

	if argv[len(argv)-1] != props {
		t.Fatalf("argv = %q, want %q last", argv, props)
	}
	// The commas here are the other reason show must not be sudo'd: they are
	// spec separators in sudoers and would need escaping to match.
	if !strings.Contains(props, ",") {
		t.Fatal("test premise broken: property list should contain commas")
	}
}

func TestSystemctlPathIsOneOfTheSudoersPaths(t *testing.T) {
	// Resolving through $PATH could land somewhere the drop-in does not
	// cover, so the candidate list is closed to the two paths it grants.
	got := systemctlPath()
	for _, p := range systemctlPaths {
		if got == p {
			return
		}
	}
	t.Fatalf("systemctlPath() = %q, not in %q", got, systemctlPaths)
}

func TestIsSudoAuthFailure(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{"password required", "sudo: a password is required", true},
		{"no tty and no askpass", "sudo: no tty present and no askpass program specified", true},
		{"terminal required", "sudo: a terminal is required to read the password", true},
		{"not allowed", "Sorry, user rt-agent is not allowed to execute '/bin/systemctl --no-pager start x.service' as root.", true},
		{"not in sudoers", "rt-agent is not in the sudoers file.", true},
		{"unit failure is not an auth failure", "Job for rt-vllm-llama3.service failed because the control process exited with error code.", false},
		{"unit not found", "Unit rt-vllm-nope.service could not be found.", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSudoAuthFailure([]byte(tt.out)); got != tt.want {
				t.Errorf("isSudoAuthFailure(%q) = %v, want %v", tt.out, got, tt.want)
			}
		})
	}
}
