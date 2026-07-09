//go:build darwin

package netown

import "os/exec"

func sampleDarwinNetstatTCP() []RawConn {
	out, err := exec.Command("/usr/sbin/netstat", "-anv", "-p", "tcp").Output()
	if err != nil {
		out, err = exec.Command("netstat", "-anv", "-p", "tcp").Output()
	}
	if err != nil {
		return nil
	}
	return parseDarwinNetstatTCP(out)
}
