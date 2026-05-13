// Package storage detects and reports NAS / network / pooled storage
// systems mounted on the node.
//
// Scope intentionally narrow: ZFS pools, NFSv3/v4 mounts, CIFS/SMB
// mounts, Ceph (cephfs/rbd), GlusterFS (fuse.glusterfs), Lustre. These
// surfaces are auto-detected from mount tables and /proc — no config —
// because the dispatcher wants to know "is the model dir backed by a
// flaky NFS mount?" without operators having to declare each box's
// storage topology.
//
// Build-tag split mirrors the rest of the agent: Linux gets the rich
// per-filesystem parsing (/proc/spl/kstat/zfs, /proc/self/mountstats);
// macOS and Windows get a capacity-only view via gopsutil's partition
// listing.
package storage

// Info is one storage entry under /health.storage[].
type Info struct {
	// Type is the canonical kind: "zfs" | "nfs" | "nfs4" | "cifs" |
	// "ceph" | "cephfs" | "glusterfs" | "lustre". Always set.
	Type string `json:"type"`

	// Mountpoint is the local path (NFS/CIFS/Ceph/Gluster). Empty for
	// raw ZFS pools that aren't mounted (datasets are; the pool itself
	// is the unit reported by ZFS-specific entries).
	Mountpoint string `json:"mountpoint,omitempty"`

	// Server is the remote endpoint (NFS/CIFS) parsed from the device
	// field (e.g. "10.0.0.5" for "10.0.0.5:/export").
	Server string `json:"server,omitempty"`

	// Export is the server-side path (NFS) or share name (CIFS).
	Export string `json:"export,omitempty"`

	// TotalGB / UsedGB / UsedPct come from statfs on the mountpoint when
	// it's a real filesystem. Omitted for raw ZFS pool entries.
	TotalGB float64 `json:"total_gb,omitempty"`
	UsedGB  float64 `json:"used_gb,omitempty"`
	UsedPct float64 `json:"used_pct,omitempty"`

	// ZFS-specific. PoolName is set for pool entries; PoolHealth comes from
	// /proc/spl/kstat/zfs/<pool>/state when available.
	PoolName   string `json:"pool_name,omitempty"`
	PoolHealth string `json:"pool_health,omitempty"`

	// NFS-specific options parsed from the mount line. Version is "3" /
	// "4" / "4.1" / "4.2" depending on what the kernel reports.
	NFSVersion string `json:"nfs_version,omitempty"`
	NFSOptions string `json:"nfs_options,omitempty"`
}

// Probe returns every detected NAS / pooled storage entry on the host.
// Empty slice (never nil) when nothing matches — keeps the JSON shape
// stable across platforms.
func Probe() []Info {
	out := probe()
	if out == nil {
		return []Info{}
	}
	return out
}
