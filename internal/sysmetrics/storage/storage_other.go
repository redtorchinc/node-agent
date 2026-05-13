//go:build !linux

package storage

import (
	"strings"

	gdisk "github.com/shirou/gopsutil/v3/disk"
)

// probe on non-Linux walks gopsutil partitions and filters to known
// network/NAS fstypes. macOS reports NFS as "nfs", SMB as "smbfs". ZFS
// is detected by fstype "zfs" (OpenZFS on macOS / FreeBSD) — kstat is
// not available off Linux, so we report mount-level capacity only.
func probe() []Info {
	parts, err := gdisk.Partitions(true) // all=true: include NFS/SMB
	if err != nil {
		return nil
	}
	var out []Info
	for _, p := range parts {
		kind := classifyFstype(p.Fstype)
		if kind == "" {
			continue
		}
		entry := Info{
			Type:       kind,
			Mountpoint: p.Mountpoint,
		}
		switch kind {
		case "nfs", "nfs4":
			server, export := splitColonPath(p.Device)
			entry.Server = server
			entry.Export = export
		case "cifs":
			server, share := splitCIFSPath(p.Device)
			entry.Server = server
			entry.Export = share
		case "zfs":
			entry.PoolName = strings.SplitN(p.Device, "/", 2)[0]
		}
		if u, err := gdisk.Usage(p.Mountpoint); err == nil && u != nil && u.Total > 0 {
			entry.TotalGB = round2(float64(u.Total) / 1024 / 1024 / 1024)
			entry.UsedGB = round2(float64(u.Used) / 1024 / 1024 / 1024)
			entry.UsedPct = round2(u.UsedPercent)
		}
		out = append(out, entry)
	}
	return out
}

func classifyFstype(t string) string {
	switch strings.ToLower(t) {
	case "nfs":
		return "nfs"
	case "nfs4":
		return "nfs4"
	case "cifs", "smbfs", "smb3":
		return "cifs"
	case "ceph":
		return "ceph"
	case "fuse.cephfs":
		return "cephfs"
	case "fuse.glusterfs":
		return "glusterfs"
	case "lustre":
		return "lustre"
	case "zfs":
		return "zfs"
	}
	return ""
}

func splitColonPath(dev string) (string, string) {
	if i := strings.Index(dev, ":"); i >= 0 {
		return dev[:i], dev[i+1:]
	}
	return "", dev
}

func splitCIFSPath(dev string) (string, string) {
	d := strings.TrimPrefix(dev, "//")
	d = strings.TrimPrefix(d, `\\`)
	if i := strings.IndexAny(d, "/\\"); i >= 0 {
		return d[:i], d[i+1:]
	}
	return d, ""
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
