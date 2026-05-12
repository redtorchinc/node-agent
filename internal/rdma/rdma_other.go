//go:build !linux

package rdma

import "context"

func probe(_ context.Context) *Info { return nil }

func available() bool { return false }
