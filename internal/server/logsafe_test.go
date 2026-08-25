package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLogSafeNeutralisesLineForging(t *testing.T) {
	// The attack this exists for: a value that closes the current record and
	// opens a forged one.
	const forged = "rt-vllm-x.service\nlevel=INFO msg=\"service action ok\" unit=sshd.service"

	got := logSafe(forged)
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("logSafe kept a newline: %q", got)
	}
	if !strings.Contains(got, "rt-vllm-x.service") {
		t.Errorf("logSafe dropped the legitimate prefix: %q", got)
	}
	// The forged text should survive as inert content — the point is that it
	// can no longer be a separate record, not that it vanishes.
	if !strings.Contains(got, "sshd.service") {
		t.Errorf("logSafe should neutralise structure, not delete content: %q", got)
	}
}

func TestLogSafe(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"ordinary unit name", "rt-vllm-llama3.service", "rt-vllm-llama3.service"},
		{"ordinary model name", "qwen3:8b-instruct", "qwen3:8b-instruct"},
		{"newline", "a\nb", "a�b"},
		{"carriage return", "a\rb", "a�b"},
		{"tab", "a\tb", "a�b"},
		{"null byte", "a\x00b", "a�b"},
		{"escape sequence", "a\x1b[31mb", "a�[31mb"},
		{"multiple controls", "\n\r\t", "���"},
		{"non-ascii is preserved", "modèle-日本語", "modèle-日本語"},
		{"spaces are fine", "a b c", "a b c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logSafe(tt.in); got != tt.want {
				t.Errorf("logSafe(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLogSafeTruncates(t *testing.T) {
	long := strings.Repeat("a", maxLoggedValue*4)
	got := logSafe(long)

	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("long value not marked truncated: %q", got[len(got)-20:])
	}
	body := strings.TrimSuffix(got, "…(truncated)")
	if n := utf8.RuneCountInString(body); n != maxLoggedValue {
		t.Errorf("kept %d runes, want %d", n, maxLoggedValue)
	}
}

func TestLogSafeTruncatesOnRuneBoundary(t *testing.T) {
	// Multi-byte runes: a byte-wise cut would leave a broken rune behind.
	long := strings.Repeat("é", maxLoggedValue*2)
	got := logSafe(long)
	body := strings.TrimSuffix(got, "…(truncated)")

	if !utf8.ValidString(body) {
		t.Errorf("truncation produced invalid UTF-8: %q", body)
	}
	if n := utf8.RuneCountInString(body); n != maxLoggedValue {
		t.Errorf("kept %d runes, want %d", n, maxLoggedValue)
	}
}

func TestLogSafeExactlyAtLimitIsNotTruncated(t *testing.T) {
	at := strings.Repeat("a", maxLoggedValue)
	if got := logSafe(at); got != at {
		t.Errorf("value of exactly maxLoggedValue was altered: len=%d", len(got))
	}
}

func TestLogSafeSlice(t *testing.T) {
	in := []string{"clean.service", "forged\nline", ""}
	got := logSafeSlice(in)

	want := []string{"clean.service", "forged�line", ""}
	if len(got) != len(want) {
		t.Fatalf("got %d elements, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The caller's slice is also headed for a JSON response body, where the
	// values must stay verbatim.
	if in[1] != "forged\nline" {
		t.Errorf("logSafeSlice mutated its input: %q", in[1])
	}
}

func TestLogSafeSliceNilStaysNil(t *testing.T) {
	if got := logSafeSlice(nil); got != nil {
		t.Errorf("logSafeSlice(nil) = %v, want nil", got)
	}
}
