//go:build !windows

package disk

import "os"

func defaultPaths() []string {
	paths := []string{"/"}
	for _, extra := range []string{"/var/lib/ollama", "/var/lib/rt-node-agent"} {
		if _, err := os.Stat(extra); err == nil {
			paths = append(paths, extra)
		}
	}
	return paths
}
