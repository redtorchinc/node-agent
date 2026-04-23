// Package buildinfo exposes version/build metadata populated at link time
// via -ldflags "-X ...". All three values are overridden in the release
// workflow; the defaults here are what unversioned local builds report.
package buildinfo

var (
	Version   = "dev"
	GitSHA    = "unknown"
	BuildTime = "unknown"
)
