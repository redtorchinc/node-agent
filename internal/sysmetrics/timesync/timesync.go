// Package timesync surfaces NTP clock-skew under /health.time_sync. Per-OS
// implementations live in source files with build tags. The shared Info
// struct is the wire contract.
package timesync

// Info is /health.time_sync. All fields may be omitted on platforms /
// configurations where the data isn't available — operators should treat
// a missing field as "unknown" rather than zero.
type Info struct {
	Source       string   `json:"source"`              // "chrony" | "timesyncd" | "sntp" | "w32tm"
	Synced       bool     `json:"synced"`
	SkewMS       *float64 `json:"skew_ms,omitempty"`   // signed offset; negative = local clock behind
	Stratum      *int     `json:"stratum,omitempty"`
	LastUpdateS  *int     `json:"last_update_s,omitempty"`
}
