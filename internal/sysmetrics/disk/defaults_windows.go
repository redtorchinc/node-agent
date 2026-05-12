//go:build windows

package disk

// defaultPaths returns the system drive plus any other lettered drive that
// holds the rt-node-agent data dir. gpsutil/disk.Partitions(false) below
// will auto-discover the rest of the drives with >= 50 GB.
func defaultPaths() []string {
	return []string{`C:\`}
}
