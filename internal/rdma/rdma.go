// Package rdma surfaces RoCE/InfiniBand fabric state from /sys/class/infiniband
// (Linux only). All non-Linux builds drop in a stub that reports `nil`.
//
// The wire shape lives here so the health package can reference it without
// importing per-OS files.
package rdma

import "context"

// Info is /health.rdma. Omitted from /health entirely (nil) on hosts
// without RDMA hardware — the dispatcher's contract is "absence means no
// RDMA on this node," NOT "an empty rdma block."
type Info struct {
	Enabled       bool                     `json:"enabled"`
	KernelModules map[string]bool          `json:"kernel_modules"`
	Devices       []Device                 `json:"devices"`

	// GPUDirectSupported reports whether the host's GPU architecture can
	// support GPUDirect RDMA (the nvidia_peermem / nv-p2p path).
	//
	//   true   discrete NVIDIA GPU: nvidia_peermem is expected
	//   false  unified-memory NVIDIA (GB10 / DGX Spark): GPUDirect RDMA is
	//          architecturally unsupported by the unified-memory iGPU; pinned
	//          memory cannot be coherently accessed by I/O peripherals. RDMA
	//          applications fall back to cudaHostAlloc + ib_reg_mr.
	//   nil    unknown (no NVIDIA GPU detected, or non-Linux)
	//
	// Filled by the health composer (internal/health) after both gpu and
	// rdma probes complete; the rdma package itself doesn't know about GPUs.
	GPUDirectSupported *bool `json:"gpu_direct_supported,omitempty"`
}

// Device is one RDMA port. Linux's /sys/class/infiniband/<dev>/ports/<port>/
// is the source for everything below; missing files cause the corresponding
// field to be left zero.
type Device struct {
	Name             string    `json:"name"`
	Port             int       `json:"port"`
	State            string    `json:"state"`           // ACTIVE | DOWN | INIT | ARMED | UNKNOWN
	PhysicalState    string    `json:"physical_state"`  // LINK_UP | DISABLED | POLLING | SLEEP | UNKNOWN
	ActiveMTU        int       `json:"active_mtu,omitempty"`
	MaxMTU           int       `json:"max_mtu,omitempty"`
	LinkLayer        string    `json:"link_layer,omitempty"`
	GIDIndex         int       `json:"gid_index,omitempty"`
	RateGbps         int       `json:"rate_gbps,omitempty"`
	Counters         Counters  `json:"counters,omitempty"`
	PauseFrames      *Pause    `json:"pause_frames,omitempty"`
	LastCollectedTS  int64     `json:"last_collected_ts"`
}

// Counters is the subset of /sys/.../counters/ files the dispatcher uses.
type Counters struct {
	PortXmitDataBytes              uint64 `json:"port_xmit_data_bytes,omitempty"`
	PortRcvDataBytes               uint64 `json:"port_rcv_data_bytes,omitempty"`
	PortXmitPackets                uint64 `json:"port_xmit_packets,omitempty"`
	PortRcvPackets                 uint64 `json:"port_rcv_packets,omitempty"`
	SymbolErrorCounter             uint64 `json:"symbol_error_counter,omitempty"`
	LinkErrorRecovery              uint64 `json:"link_error_recovery,omitempty"`
	LinkDowned                     uint64 `json:"link_downed,omitempty"`
	PortRcvErrors                  uint64 `json:"port_rcv_errors,omitempty"`
	ExcessiveBufferOverrunErrors   uint64 `json:"excessive_buffer_overrun_errors,omitempty"`
}

// Pause carries PFC pause-frame counts. Rate fields are computed over a
// 60s sliding window from cumulative counters captured by the collector.
type Pause struct {
	Rx      uint64 `json:"rx,omitempty"`
	Tx      uint64 `json:"tx,omitempty"`
	RxRate  uint64 `json:"rx_rate"`
	TxRate  uint64 `json:"tx_rate"`
}

// Probe returns the current RDMA snapshot, or nil when no IB devices are
// present. ctx is honored for the per-file reads (sysfs is synchronous so
// the deadline is mostly insurance against a stuck driver).
//
// Implementation lives in rdma_linux.go; non-Linux platforms get a nil
// stub from rdma_other.go.
func Probe(ctx context.Context) *Info { return probe(ctx) }

// Available reports whether the agent can probe RDMA on this host (any IB
// device dir present). Cached behind a single stat in the Linux file.
func Available() bool { return available() }
