//go:build !linux

package netown

// fillCgroupInfo is Linux-only: launchd/Windows have no cgroup analogue
// the agent reads today, so service/container fields stay empty and the
// response documents that via docs/api/network-flows.md §Platform notes.
func fillCgroupInfo(pid int32, info *ProcInfo) {}
