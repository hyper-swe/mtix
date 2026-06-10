// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package sqlite

import (
	"fmt"
	"syscall"
)

// freeDiskSpace returns the bytes available to unprivileged callers on the
// volume holding dir. Uses Bavail (not Bfree) so root-reserved blocks do
// not inflate the answer.
func freeDiskSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // Bsize is never negative
}
