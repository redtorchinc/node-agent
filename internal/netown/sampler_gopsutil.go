package netown

import (
	"runtime"
	"strings"
	"syscall"

	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

// gopsutilSampler reads the socket table via gopsutil — procfs on Linux,
// lsof on macOS, IP Helper (iphlpapi) on Windows. No new dependency: the
// same module already backs cpu/mem/process elsewhere in the agent.
type gopsutilSampler struct{}

func newGopsutilSampler() Sampler { return gopsutilSampler{} }

func (gopsutilSampler) Sample() ([]RawConn, error) {
	conns, err := gnet.Connections("inet") // tcp+udp, v4+v6
	if err != nil {
		return nil, err
	}
	out := make([]RawConn, 0, len(conns))
	for _, c := range conns {
		var proto string
		switch c.Type {
		case syscall.SOCK_STREAM:
			proto = "tcp"
		case syscall.SOCK_DGRAM:
			proto = "udp"
		default:
			continue
		}
		out = append(out, RawConn{
			Proto:      proto,
			LocalAddr:  c.Laddr.IP,
			LocalPort:  c.Laddr.Port,
			RemoteAddr: c.Raddr.IP,
			RemotePort: c.Raddr.Port,
			State:      strings.ToLower(c.Status),
			PID:        c.Pid,
		})
	}
	return out, nil
}

func (gopsutilSampler) Source() string {
	switch runtime.GOOS {
	case "linux":
		return "procfs"
	case "darwin":
		return "lsof"
	case "windows":
		return "iphlpapi"
	default:
		return "gopsutil"
	}
}

// gopsutilProcs enriches PIDs via gopsutil/process, plus the per-OS
// cgroup/service/container parse (procCgroupInfo — Linux only, no-op
// elsewhere).
type gopsutilProcs struct{}

func newGopsutilProcs() ProcResolver { return gopsutilProcs{} }

func (gopsutilProcs) Info(pid int32) (ProcInfo, error) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return ProcInfo{}, err
	}
	info := ProcInfo{}
	// Name is the cheapest field and the strongest signal that the PID is
	// readable at all — everything after it is best-effort.
	info.Name, err = p.Name()
	if err != nil {
		return ProcInfo{}, err
	}
	info.Exe, _ = p.Exe()
	info.User, _ = p.Username()
	if uids, err := p.Uids(); err == nil && len(uids) > 1 {
		uid := uids[1] // effective UID
		info.UID = &uid
	}
	info.CmdlineRaw, _ = p.CmdlineSlice()
	fillCgroupInfo(pid, &info)
	return info, nil
}
