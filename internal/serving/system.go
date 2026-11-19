package serving

import (
	"strings"

	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"github.com/redtorchinc/node-agent/internal/netown"
)

// System returns Deps backed by gopsutil and procfs. Reading another user's
// sockets needs the CAP_SYS_PTRACE / CAP_DAC_READ_SEARCH the systemd unit
// already grants for /network attribution; without them Listeners is empty
// and services are still named, just not probed.
func System() Deps { return systemDeps{} }

type systemDeps struct{}

func (systemDeps) Proc(pid int32) (Proc, bool) {
	p, err := process.NewProcess(pid)
	if err != nil {
		return Proc{}, false
	}
	name, err := p.Name()
	if err != nil {
		return Proc{}, false
	}
	ppid, _ := p.Ppid()
	argv, _ := p.CmdlineSlice()
	created, _ := p.CreateTime()
	return Proc{PPID: ppid, Name: name, Cmdline: argv, Created: created}, true
}

func (systemDeps) Listeners(pid int32) []Listener {
	conns, err := gnet.ConnectionsPid("tcp", pid)
	if err != nil {
		return nil
	}
	var out []Listener
	for _, c := range conns {
		if strings.EqualFold(c.Status, "LISTEN") {
			out = append(out, Listener{Addr: c.Laddr.IP, Port: c.Laddr.Port})
		}
	}
	return out
}

func (systemDeps) Unit(pid int32) (string, string) { return netown.ServiceOf(pid) }

func (systemDeps) ServingCandidates() []int32 {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	var out []int32
	for _, p := range procs {
		name, err := p.Name()
		if err != nil || isWorkerTitle(name) {
			continue
		}
		argv, _ := p.CmdlineSlice()
		if servingKinds[Classify(name, argv)] {
			out = append(out, p.Pid)
		}
	}
	return out
}
