// Package safecast holds saturating integer conversions.
//
// The agent's whole job is reading numbers off the host — gopsutil, statfs,
// /proc, nvidia-smi — and most of them arrive as uint64 while the wire
// contract in spec/SPEC.md is JSON-friendly int64. A bare int64(x) cast is
// silently wrong at the edges: a uint64 above MaxInt64 lands as a negative,
// and a negative int64 cast to uint64 becomes astronomically large. Either
// way a bogus value reaches /health, and the backend's ranker acts on it.
//
// None of these overflows are reachable today with real hardware values
// (see the individual doc comments). They exist because "unreachable" is a
// property of the current callers, not of the conversion, and because
// stating the clamp explicitly is what makes gosec's G115 findings
// reviewable instead of 14 identical suppressions.
//
// Saturate rather than error: a metric that pins at the maximum is more
// useful to a ranker than a probe that fails outright, and these paths have
// no way to surface an error that a caller would act on.
package safecast

import "math"

// I64 converts a uint64 to int64, saturating at math.MaxInt64 instead of
// wrapping negative.
//
// Reachable only above 9.2 × 10^18. Real callers pass byte counts and
// second counts, so hitting it needs ~8 exabytes or ~292 billion years.
func I64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// U64 converts an int64 to uint64, clamping negatives to 0 instead of
// wrapping to a huge positive.
//
// Callers pass values that are non-negative in practice (statfs block
// sizes, a post-epoch Unix timestamp). The clamp matters because the wrap
// is so much worse than the clamp: a -1 block size becomes 1.8 × 10^19
// rather than 0, and that propagates into a capacity figure.
func U64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// BytesToMB converts a byte count to whole mebibytes as int64.
//
// This is the agent's single most common conversion — RAM, swap, and
// per-process RSS all land on the wire as *_mb. Dividing first means the
// saturation in I64 is unreachable for any real byte count, but routing
// through it keeps one rule for the whole family.
func BytesToMB(b uint64) int64 {
	return I64(b / 1024 / 1024)
}
