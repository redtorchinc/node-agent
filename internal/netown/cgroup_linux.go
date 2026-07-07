//go:build linux

package netown

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// fillCgroupInfo parses /proc/<pid>/cgroup into the systemd unit and
// container identity. cgroup v2 has a single "0::<path>" line; on v1 the
// systemd hierarchy line ("name=systemd") carries the same path shape.
func fillCgroupInfo(pid int32, info *ProcInfo) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return
	}
	path := cgroupPath(string(b))
	if path == "" {
		return
	}
	info.Cgroup = path
	info.Service = unitFromCgroup(path)
	info.ContainerID = containerFromCgroup(path)
}

// cgroupPath picks the most useful hierarchy path from the cgroup file:
// the v2 unified line if present, else the v1 systemd line.
func cgroupPath(contents string) string {
	var v1systemd string
	for _, line := range strings.Split(contents, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			return parts[2] // cgroup v2 unified
		case strings.Contains(parts[1], "name=systemd"):
			v1systemd = parts[2]
		}
	}
	return v1systemd
}

// unitFromCgroup extracts the innermost systemd unit (.service or .scope)
// from a cgroup path like /system.slice/rt-vllm-example.service.
func unitFromCgroup(path string) string {
	segs := strings.Split(path, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if strings.HasSuffix(s, ".service") || strings.HasSuffix(s, ".scope") {
			// Strip systemd's escaped prefix forms like
			// docker-<id>.scope handled separately by containerFromCgroup.
			return s
		}
	}
	return ""
}

var containerIDRe = regexp.MustCompile(`(?:docker-|cri-containerd-|crio-|libpod-)?([0-9a-f]{64})(?:\.scope)?$`)

// containerFromCgroup pulls a 64-hex container id out of docker/containerd/
// cri-o/podman cgroup naming. Empty when the process isn't containerized.
func containerFromCgroup(path string) string {
	for _, seg := range strings.Split(path, "/") {
		if m := containerIDRe.FindStringSubmatch(seg); m != nil {
			return m[1]
		}
	}
	return ""
}
