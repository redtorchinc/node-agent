//go:build linux

package rdma

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const sysIB = "/sys/class/infiniband"

var kernelModuleList = []string{"mlx5_ib", "mlx5_core", "nvidia_peermem", "ib_core", "ib_uverbs"}

func available() bool {
	fi, err := os.Stat(sysIB)
	if err != nil || !fi.IsDir() {
		return false
	}
	ents, err := os.ReadDir(sysIB)
	if err != nil {
		return false
	}
	return len(ents) > 0
}

func probe(_ context.Context) *Info {
	if !available() {
		return nil
	}
	info := &Info{
		Enabled:       true,
		KernelModules: probeKernelModules(),
		Devices:       []Device{},
	}

	ents, err := os.ReadDir(sysIB)
	if err != nil {
		return info
	}
	for _, e := range ents {
		devDir := filepath.Join(sysIB, e.Name())
		portsDir := filepath.Join(devDir, "ports")
		ports, err := os.ReadDir(portsDir)
		if err != nil {
			continue
		}
		for _, p := range ports {
			port, err := strconv.Atoi(p.Name())
			if err != nil {
				continue
			}
			d := Device{
				Name:            e.Name(),
				Port:            port,
				LastCollectedTS: time.Now().Unix(),
			}
			d.State = parseIBState(readSysfs(filepath.Join(portsDir, p.Name(), "state")))
			d.PhysicalState = parsePhysState(readSysfs(filepath.Join(portsDir, p.Name(), "phys_state")))
			d.LinkLayer = strings.TrimSpace(readSysfs(filepath.Join(portsDir, p.Name(), "link_layer")))
			d.RateGbps = parseRateGbps(readSysfs(filepath.Join(portsDir, p.Name(), "rate")))
			d.Counters = readCounters(filepath.Join(portsDir, p.Name(), "counters"))
			info.Devices = append(info.Devices, d)
		}
	}
	return info
}

func probeKernelModules() map[string]bool {
	// Cheap heuristic: look for /sys/module/<name>. Avoids parsing
	// /proc/modules and matches what `lsmod` reports without the formatting.
	out := map[string]bool{}
	for _, m := range kernelModuleList {
		_, err := os.Stat(filepath.Join("/sys/module", m))
		out[m] = err == nil
	}
	return out
}

func readSysfs(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// /sys/.../state reads as "4: ACTIVE" or "1: DOWN".
func parseIBState(s string) string {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return strings.ToUpper(s)
	}
	return strings.ToUpper(strings.TrimSpace(parts[1]))
}

// /sys/.../phys_state reads as "5: LinkUp" — normalise to LINK_UP / DISABLED / POLLING / SLEEP.
func parsePhysState(s string) string {
	parts := strings.SplitN(s, ":", 2)
	v := s
	if len(parts) == 2 {
		v = strings.TrimSpace(parts[1])
	}
	switch strings.ToLower(v) {
	case "linkup":
		return "LINK_UP"
	case "disabled":
		return "DISABLED"
	case "polling":
		return "POLLING"
	case "sleep":
		return "SLEEP"
	default:
		return "UNKNOWN"
	}
}

// "200 Gb/sec (4X EDR)" → 200
func parseRateGbps(s string) int {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0
	}
	return v
}

func readCounters(dir string) Counters {
	var c Counters
	c.PortXmitDataBytes = readUint(filepath.Join(dir, "port_xmit_data"))
	c.PortRcvDataBytes = readUint(filepath.Join(dir, "port_rcv_data"))
	c.PortXmitPackets = readUint(filepath.Join(dir, "port_xmit_packets"))
	c.PortRcvPackets = readUint(filepath.Join(dir, "port_rcv_packets"))
	c.SymbolErrorCounter = readUint(filepath.Join(dir, "symbol_error"))
	c.LinkErrorRecovery = readUint(filepath.Join(dir, "link_error_recovery"))
	c.LinkDowned = readUint(filepath.Join(dir, "link_downed"))
	c.PortRcvErrors = readUint(filepath.Join(dir, "port_rcv_errors"))
	c.ExcessiveBufferOverrunErrors = readUint(filepath.Join(dir, "excessive_buffer_overrun_errors"))
	return c
}

func readUint(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
