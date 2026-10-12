// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// changeTime reads the inode change time from the opened descriptor (FR-19).
func changeTime(f *os.File) (time.Time, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil { //nolint:gosec // descriptor values fit in int
		return time.Time{}, err
	}
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)), nil
}
