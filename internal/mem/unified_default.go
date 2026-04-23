//go:build !(darwin && arm64)

package mem

func unified() bool { return false }
