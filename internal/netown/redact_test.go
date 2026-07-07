package netown

import (
	"strings"
	"testing"
)

func TestRedactCmdline_InlineKV(t *testing.T) {
	got := RedactCmdline([]string{"python", "--api-key=sk-abc123", "--port=8000"}, 240)
	if strings.Contains(got, "sk-abc123") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "--api-key=[redacted]") || !strings.Contains(got, "--port=8000") {
		t.Errorf("got %q", got)
	}
}

func TestRedactCmdline_FlagThenValue(t *testing.T) {
	got := RedactCmdline([]string{"vllm", "serve", "--token", "hunter2", "--model", "qwen"}, 240)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "--token [redacted]") || !strings.Contains(got, "--model qwen") {
		t.Errorf("got %q", got)
	}
}

func TestRedactCmdline_EnvStyleAndBearer(t *testing.T) {
	got := RedactCmdline([]string{"env", "DB_PASSWORD=hunter2", "AUTH_TOKEN:abc", "run"}, 240)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abc") {
		t.Fatalf("secret leaked: %q", got)
	}
	got = RedactCmdline([]string{"curl", "-H", "Authorization:", "Bearer", "sk-live-9999"}, 240)
	if strings.Contains(got, "sk-live-9999") {
		t.Fatalf("bearer token leaked: %q", got)
	}
}

func TestRedactCmdline_TruncationAfterRedaction(t *testing.T) {
	long := []string{"cmd", "--secret=" + strings.Repeat("x", 500)}
	got := RedactCmdline(long, 40)
	if strings.Contains(got, "xxx") {
		t.Fatalf("truncation must not beat redaction: %q", got)
	}
	if len(got) > 40+len("…") {
		t.Errorf("len=%d over cap: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "…") && len("cmd --secret=[redacted]") > 40 {
		t.Errorf("cut string must carry ellipsis: %q", got)
	}
}

func TestRedactCmdline_PlainUntouched(t *testing.T) {
	got := RedactCmdline([]string{"nginx", "-g", "daemon off;"}, 240)
	if got != "nginx -g daemon off;" {
		t.Errorf("got %q", got)
	}
	if RedactCmdline(nil, 240) != "" {
		t.Error("nil argv must be empty")
	}
}

func TestTruncateBytes_RuneSafe(t *testing.T) {
	// £ occupies bytes 2-3; a cut at 3 would split it.
	if got := truncateBytes("ab£cd", 3); got != "ab…" {
		t.Errorf("truncateBytes = %q, want %q", got, "ab…")
	}
	if got := truncateBytes("abc", 3); got != "abc" {
		t.Errorf("exact fit must not truncate: %q", got)
	}
}
