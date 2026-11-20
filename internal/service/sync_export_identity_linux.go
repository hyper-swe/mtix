// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package service

import (
	"os"
	"syscall"
)

// fileIdentityOf returns the change time (nanoseconds), inode and device of a
// stat result. ok is false when the platform gave none (MTIX-130.20.2).
func fileIdentityOf(info os.FileInfo) (ctimeNS int64, ino, dev uint64, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, 0, false
	}
	return st.Ctim.Nano(), uint64(st.Ino), uint64(st.Dev), true //nolint:unconvert // field widths differ by architecture
}
