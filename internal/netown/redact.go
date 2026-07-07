package netown

import (
	"regexp"
	"strings"
)

// Command lines routinely carry secrets (--api-key=..., TOKEN=..., -p
// hunter2). These endpoints are Bearer-gated, but redaction is still
// mandatory before anything reaches the wire — the backend stores these
// strings beside flow records, and the retention story there is not this
// agent's to control. Redaction happens BEFORE truncation so a secret is
// never partially exposed by the byte cap.

const redacted = "[redacted]"

// sensitiveKeyRe matches argument keys whose value must not be emitted.
var sensitiveKeyRe = regexp.MustCompile(
	`(?i)^-{0,2}(?:[A-Z0-9_.-]*(?:api[_-]?key|apikey|secret|token|passw(?:or)?d|credential|private[_-]?key|auth))$`)

// sensitiveInlineRe matches key=value (or key:value) pairs anywhere in a
// single argument, e.g. "--api-key=abc" or "DB_PASSWORD=hunter2".
var sensitiveInlineRe = regexp.MustCompile(
	`(?i)([A-Z0-9_.-]*(?:api[_-]?key|apikey|secret|token|passw(?:or)?d|credential|private[_-]?key|auth)[=:])\S+`)

// bearerRe catches "Bearer <token>" sequences that survive as separate args.
var bearerRe = regexp.MustCompile(`(?i)^bearer$`)

// RedactCmdline joins argv into a display string with secret-shaped
// values replaced, then truncates to maxBytes (appending "…" when cut).
func RedactCmdline(argv []string, maxBytes int) string {
	if len(argv) == 0 {
		return ""
	}
	out := make([]string, len(argv))
	redactNext := false
	for i, a := range argv {
		switch {
		case redactNext:
			out[i] = redacted
			redactNext = false
		case sensitiveKeyRe.MatchString(a):
			// Flag-style key ("--token"); the secret is the NEXT arg.
			out[i] = a
			redactNext = true
		case bearerRe.MatchString(a):
			out[i] = a
			redactNext = true
		default:
			out[i] = sensitiveInlineRe.ReplaceAllString(a, "${1}"+redacted)
		}
	}
	return truncateBytes(strings.Join(out, " "), maxBytes)
}

// truncateBytes cuts s at maxBytes without splitting a UTF-8 rune,
// appending "…" when anything was dropped.
func truncateBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
