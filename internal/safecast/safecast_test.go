package safecast

import (
	"math"
	"testing"
)

func TestI64(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want int64
	}{
		{"zero", 0, 0},
		{"small", 4096, 4096},
		{"max int64 exactly", math.MaxInt64, math.MaxInt64},
		// The whole point: a bare int64() cast here yields -1.
		{"max int64 plus one saturates", math.MaxInt64 + 1, math.MaxInt64},
		{"max uint64 saturates", math.MaxUint64, math.MaxInt64},
		{"realistic 2TB of RAM in bytes", 2 << 40, 2 << 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := I64(tt.in); got != tt.want {
				t.Errorf("I64(%d) = %d, want %d", tt.in, got, tt.want)
			}
			if got := I64(tt.in); got < 0 {
				t.Errorf("I64(%d) = %d, must never be negative", tt.in, got)
			}
		})
	}
}

func TestU64(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want uint64
	}{
		{"zero", 0, 0},
		{"typical statfs block size", 4096, 4096},
		{"max int64", math.MaxInt64, math.MaxInt64},
		// A bare uint64() cast here yields 18446744073709551615, which then
		// multiplies into a nonsense capacity figure.
		{"negative one clamps", -1, 0},
		{"min int64 clamps", math.MinInt64, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := U64(tt.in); got != tt.want {
				t.Errorf("U64(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestBytesToMB(t *testing.T) {
	tests := []struct {
		name string
		in   uint64
		want int64
	}{
		{"zero", 0, 0},
		{"sub-MB truncates down", 1024*1024 - 1, 0},
		{"exactly 1MB", 1024 * 1024, 1},
		{"1GB", 1024 * 1024 * 1024, 1024},
		{"128GB, a plausible GB10 figure", 128 * 1024 * 1024 * 1024, 128 * 1024},
		{"max uint64 does not wrap", math.MaxUint64, math.MaxUint64 / 1024 / 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BytesToMB(tt.in); got != tt.want {
				t.Errorf("BytesToMB(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// U64 and I64 should round-trip any non-negative int64, since that is the
// range every caller actually operates in.
func TestRoundTripNonNegative(t *testing.T) {
	for _, v := range []int64{0, 1, 4096, 1 << 30, math.MaxInt64} {
		if got := I64(U64(v)); got != v {
			t.Errorf("I64(U64(%d)) = %d, want %d", v, got, v)
		}
	}
}
