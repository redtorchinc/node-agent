// Package network reports basic interface state for /health.network. Used
// for fleet-wide visibility (link up/down, rough rx/tx) but NOT for
// degraded_reasons in v0.2.0 — informational only.
package network

import (
	"net"
	"os"
	"strings"

	gnet "github.com/shirou/gopsutil/v3/net"
)

// Info is /health.network.
type Info struct {
	HostnameFQDN string      `json:"hostname_fqdn,omitempty"`
	Interfaces   []Interface `json:"interfaces"`
}

// Interface is one network interface entry. Rates are not yet computed in
// v0.2.0 — surfacing them requires a background ticker which we'll add in
// a follow-up. RxBytesTotal/TxBytesTotal are cumulative since boot.
type Interface struct {
	Name          string   `json:"name"`
	Up            bool     `json:"up"`
	SpeedMbps     int      `json:"speed_mbps,omitempty"`
	MTU           int      `json:"mtu,omitempty"`
	IPv4          []string `json:"ipv4,omitempty"`
	IPv6          []string `json:"ipv6,omitempty"`
	RxBytesTotal  uint64   `json:"rx_bytes_total,omitempty"`
	TxBytesTotal  uint64   `json:"tx_bytes_total,omitempty"`
	RxErrorsTotal uint64   `json:"rx_errors_total,omitempty"`
	TxErrorsTotal uint64   `json:"tx_errors_total,omitempty"`
}

// Probe returns interface state. Best-effort; fields default to zero on
// any source error.
//
// macOS-specific note: an earlier version called `net.LookupHost(h)` to
// "confirm" the hostname resolved — except both branches set
// HostnameFQDN to the unqualified hostname anyway, so the DNS
// round-trip was pure dead weight. On darwin without a configured
// resolver for the local hostname, that lookup blocked for the full 5s
// resolver timeout and dominated /health latency. Removed; we emit the
// unqualified hostname directly.
func Probe() Info {
	out := Info{Interfaces: []Interface{}}
	if h, err := os.Hostname(); err == nil {
		out.HostnameFQDN = h
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}

	// Counters keyed by name for cheap lookup.
	io, _ := gnet.IOCounters(true)
	byName := map[string]gnet.IOCountersStat{}
	for _, c := range io {
		byName[c.Name] = c
	}

	for _, iface := range ifaces {
		// Skip loopback and "down" link-layer interfaces noisily — they
		// dominate the list on most boxes and add no value.
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		entry := Interface{
			Name: iface.Name,
			Up:   iface.Flags&net.FlagUp != 0,
			MTU:  iface.MTU,
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ipn.IP.To4() != nil {
					entry.IPv4 = append(entry.IPv4, ipn.IP.String())
				} else if !ipn.IP.IsLinkLocalUnicast() {
					entry.IPv6 = append(entry.IPv6, ipn.IP.String())
				}
			}
		}
		if c, ok := byName[iface.Name]; ok {
			entry.RxBytesTotal = c.BytesRecv
			entry.TxBytesTotal = c.BytesSent
			entry.RxErrorsTotal = c.Errin
			entry.TxErrorsTotal = c.Errout
		}
		// Skip purely virtual no-traffic interfaces that are flagged "up" but
		// have no addresses (containers/bridges in pristine state).
		if !entry.Up && entry.RxBytesTotal == 0 && entry.TxBytesTotal == 0 {
			continue
		}
		// Suppress obvious internal-only virtual ifs to keep payload short.
		if strings.HasPrefix(entry.Name, "docker") || strings.HasPrefix(entry.Name, "br-") ||
			strings.HasPrefix(entry.Name, "veth") {
			continue
		}
		out.Interfaces = append(out.Interfaces, entry)
	}
	return out
}
