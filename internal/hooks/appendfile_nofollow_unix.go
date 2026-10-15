// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"syscall"
)

const (
	// nonBlockFlag keeps the open of a pipe without a reader from blocking.
	nonBlockFlag = syscall.O_NONBLOCK
)

// clearNonBlock returns the descriptor to blocking mode before writing.
func clearNonBlock(f *os.File) error {
	if err := syscall.SetNonblock(int(f.Fd()), false); err != nil { //nolint:gosec // G115: descriptors fit in int
		return fmt.Errorf("clear non-blocking mode: %w", err)
	}
	return nil
}

// linkCount returns the number of hard links of the file, or 1 when unknown.
func linkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink) //nolint:gosec // G115: link counts are small and non-negative
	}
	return 1
}

// wideMode reports an existing file readable or writable by group or others.
func wideMode(info os.FileInfo) bool { return info.Mode().Perm()&0o077 != 0 }
