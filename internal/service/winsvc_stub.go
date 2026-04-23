//go:build !windows

package service

import "context"

// RunIfWindowsService is a no-op on non-Windows platforms. The real
// implementation lives in winsvc.go.
func RunIfWindowsService(_ context.Context, _ any, _ any) (bool, error) {
	return false, nil
}
