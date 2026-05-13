//go:build linux

package storage

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// probe enumerates network/pooled storage by combining three sources:
//   - /proc/self/mounts for live mounts (NFS, CIFS, Ceph, GlusterFS,
//     Lustre, ZFS datasets)
//   - /proc/spl/kstat/zfs for ZFS pool presence + health
//
// statfs on each mountpoint fills capacity. Anything that fails is
// silently skipped — never fabricate a zero.
func probe() []Info {
	out := []Info{}

	mounts := readMounts()
	for _, m := range mounts {
		switch m.fstype {
		case "nfs":
			out = append(out, buildNFSEntry(m, "3"))
		case "nfs4":
			ver := nfsVersionFromOpts(m.opts)
			if ver == "" {
				ver = "4"
			}
			out = append(out, buildNFSEntry(m, ver))
		case "cifs", "smb3", "smbfs":
			out = append(out, buildCIFSEntry(m))
		case "ceph":
			out = append(out, buildGenericNetEntry(m, "ceph"))
		case "fuse.cephfs":
			out = append(out, buildGenericNetEntry(m, "cephfs"))
		case "fuse.glusterfs":
			out = append(out, buildGenericNetEntry(m, "glusterfs"))
		case "lustre":
			out = append(out, buildGenericNetEntry(m, "lustre"))
		case "zfs":
			out = append(out, buildZFSDatasetEntry(m))
		}
	}

	// Add a per-pool entry for ZFS pools that have kstat presence. This
	// surfaces health even if no dataset is currently mounted from the pool.
	for _, pool := range readZFSPools() {
		out = append(out, Info{
			Type:       "zfs",
			PoolName:   pool.name,
			PoolHealth: pool.health,
		})
	}

	return out
}

type mountEntry struct {
	device, mountpoint, fstype, opts string
}

func readMounts() []mountEntry {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []mountEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		out = append(out, mountEntry{
			device:     fields[0],
			mountpoint: fields[1],
			fstype:     fields[2],
			opts:       fields[3],
		})
	}
	return out
}

func buildNFSEntry(m mountEntry, version string) Info {
	server, export := splitColonPath(m.device)
	info := Info{
		Type:       fstypeToWire(m.fstype),
		Mountpoint: m.mountpoint,
		Server:     server,
		Export:     export,
		NFSVersion: version,
		NFSOptions: trimOpts(m.opts),
	}
	fillCapacity(&info, m.mountpoint)
	return info
}

func buildCIFSEntry(m mountEntry) Info {
	// CIFS device looks like "//server/share"
	server, export := splitCIFSPath(m.device)
	info := Info{
		Type:       "cifs",
		Mountpoint: m.mountpoint,
		Server:     server,
		Export:     export,
	}
	fillCapacity(&info, m.mountpoint)
	return info
}

func buildGenericNetEntry(m mountEntry, kind string) Info {
	info := Info{
		Type:       kind,
		Mountpoint: m.mountpoint,
	}
	// Best-effort server extraction (Ceph device: "10.0.0.1:6789:/")
	if i := strings.Index(m.device, ":"); i > 0 {
		info.Server = m.device[:i]
	}
	fillCapacity(&info, m.mountpoint)
	return info
}

func buildZFSDatasetEntry(m mountEntry) Info {
	info := Info{
		Type:       "zfs",
		Mountpoint: m.mountpoint,
		PoolName:   strings.SplitN(m.device, "/", 2)[0],
	}
	fillCapacity(&info, m.mountpoint)
	return info
}

func fillCapacity(info *Info, mountpoint string) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mountpoint, &st); err != nil {
		return
	}
	total := uint64(st.Bsize) * st.Blocks
	free := uint64(st.Bsize) * st.Bfree
	used := total - free
	if total == 0 {
		return
	}
	info.TotalGB = round2(float64(total) / 1024 / 1024 / 1024)
	info.UsedGB = round2(float64(used) / 1024 / 1024 / 1024)
	info.UsedPct = round2(float64(used) / float64(total) * 100)
}

// nfsVersionFromOpts parses "vers=4.2" or "nfsvers=4.1" from the mount
// options string.
func nfsVersionFromOpts(opts string) string {
	for _, kv := range strings.Split(opts, ",") {
		switch {
		case strings.HasPrefix(kv, "vers="):
			return strings.TrimPrefix(kv, "vers=")
		case strings.HasPrefix(kv, "nfsvers="):
			return strings.TrimPrefix(kv, "nfsvers=")
		}
	}
	return ""
}

// trimOpts returns a short summary of mount options. Full /proc/self/mounts
// opts can be > 200 chars on NFS (timeo, retrans, hard, fsc, ac…); we cap
// to keep /health small.
func trimOpts(opts string) string {
	const max = 120
	if len(opts) > max {
		return opts[:max] + "…"
	}
	return opts
}

// fstypeToWire normalises the raw fstype to the wire spelling.
func fstypeToWire(t string) string {
	switch t {
	case "nfs":
		return "nfs"
	case "nfs4":
		return "nfs4"
	}
	return t
}

// splitColonPath turns "server:/export/path" into ("server", "/export/path").
// IPv6 addresses are wrapped in brackets in the device field already, so
// the simple split holds.
func splitColonPath(dev string) (string, string) {
	if i := strings.Index(dev, ":"); i >= 0 {
		return dev[:i], dev[i+1:]
	}
	return "", dev
}

// splitCIFSPath turns "//server/share" into ("server", "share").
func splitCIFSPath(dev string) (string, string) {
	d := strings.TrimPrefix(dev, "//")
	d = strings.TrimPrefix(d, `\\`)
	if i := strings.IndexAny(d, "/\\"); i >= 0 {
		return d[:i], d[i+1:]
	}
	return d, ""
}

type zfsPool struct {
	name, health string
}

// readZFSPools enumerates pools by walking /proc/spl/kstat/zfs/. Each pool
// has a directory; the "state" file (where present) holds the health
// ("ONLINE", "DEGRADED", "FAULTED", …). Returns empty slice when ZFS
// modules aren't loaded.
func readZFSPools() []zfsPool {
	entries, err := os.ReadDir("/proc/spl/kstat/zfs")
	if err != nil {
		return nil
	}
	var pools []zfsPool
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := zfsPool{name: e.Name()}
		if b, err := os.ReadFile(filepath.Join("/proc/spl/kstat/zfs", e.Name(), "state")); err == nil {
			p.health = strings.TrimSpace(string(b))
		}
		pools = append(pools, p)
	}
	return pools
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
