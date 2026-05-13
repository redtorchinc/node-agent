package ollama

import "testing"

func TestIsStale(t *testing.T) {
	cases := []struct {
		name     string
		last     int64
		interval int64
		now      int64
		want     bool
	}{
		{"fresh", 1000, 5, 1003, false},
		{"one_interval_old", 1000, 5, 1005, false},
		{"three_intervals_old", 1000, 5, 1015, false},
		{"just_past_threshold", 1000, 5, 1016, true},
		{"hour_old", 1000, 5, 4600, true},
		{"never_scraped", 0, 5, 1000, false},
		{"zero_interval", 1000, 0, 1100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isStale(tc.last, tc.interval, tc.now)
			if got != tc.want {
				t.Errorf("isStale(last=%d, interval=%d, now=%d) = %v, want %v",
					tc.last, tc.interval, tc.now, got, tc.want)
			}
		})
	}
}
