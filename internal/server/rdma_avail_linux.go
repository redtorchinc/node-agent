//go:build linux

package server

import "os"

// rdmaAvailable detects RDMA presence by stat'ing /sys/class/infiniband.
// Hosts without the kernel modules loaded (and therefore no IB devices)
// won't have this directory; bare metal without an RDMA NIC won't either.
// Cheap (one syscall) and stable across kernels.
func rdmaAvailable() bool {
	fi, err := os.Stat("/sys/class/infiniband")
	if err != nil || !fi.IsDir() {
		return false
	}
	ents, err := os.ReadDir("/sys/class/infiniband")
	if err != nil {
		return false
	}
	return len(ents) > 0
}
