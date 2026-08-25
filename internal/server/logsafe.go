package server

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxLoggedValue caps a caller-supplied string in the log. Generous enough
// for any real unit name, model name, or run_id, small enough that a caller
// cannot use the log as a write amplifier.
const maxLoggedValue = 256

// logSafe neutralises a caller-supplied string before it reaches slog.
//
// Control characters are replaced with U+FFFD and the result is capped at
// maxLoggedValue runes. Replacing rather than stripping is deliberate: a
// value that arrived with a newline in it still *looks* wrong in the log,
// instead of silently reading as a clean value.
//
// Threat model, stated honestly: this is defence in depth, not a live
// vulnerability fix. Every endpoint that logs caller input is Bearer-gated,
// so an attacker needs the token first; and cmd/rt-node-agent installs a
// slog.TextHandler, which already quotes any value containing a character
// below U+0020. So a forged log line is not reachable today.
//
// It is here for three reasons that outlive that reasoning:
//
//  1. The safety currently depends on which handler main() happens to
//     install. Swap in a bare handler, or log with fmt, and the escaping
//     silently disappears. This makes the property local to the call site.
//  2. Quoting stops a forged *line* but not an unbounded one — a 10 MB
//     model name was still a 10 MB log record.
//  3. It is what CodeQL's go/log-injection can actually see. Twelve alerts
//     that are all "fine, because of a handler configured in another
//     package" is a Security tab nobody reads.
//
// Not for the non-string values alongside these (status codes, durations,
// booleans) — those cannot carry structure.
func logSafe(s string) string {
	if s == "" {
		return ""
	}

	truncated := false
	if utf8.RuneCountInString(s) > maxLoggedValue {
		// Cut on a rune boundary, not a byte one.
		count := 0
		for i := range s {
			if count == maxLoggedValue {
				s = s[:i]
				break
			}
			count++
		}
		truncated = true
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			// Invalid UTF-8 in the input; range yields RuneError per bad byte.
			b.WriteRune('�')
		case unicode.IsControl(r):
			// Covers \n and \r (line forging) and \t (column forging).
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if truncated {
		out += "…(truncated)"
	}
	return out
}

// logSafeSlice applies logSafe to every element. Returns a new slice; the
// caller's is untouched, since these values are also on their way to a JSON
// response where they must stay verbatim.
func logSafeSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = logSafe(s)
	}
	return out
}
