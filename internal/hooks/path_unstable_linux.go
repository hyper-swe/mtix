// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package hooks

import (
	"path/filepath"
	"syscall"
)

// procSuperMagic identifies the per-process pseudo filesystem.
const procSuperMagic = 0x9fa0

// unstableLink reports whether a link lives on the per-process pseudo
// filesystem, where a link names a different file for each process (the
// checking process is not the launched one). It is decided by the
// filesystem the link sits on (its directory, since the link itself would be
// followed), not by its text (FR-19.2).
func unstableLink(link string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(link), &st); err != nil {
		return true
	}
	return int64(st.Type) == procSuperMagic //nolint:gosec // G115: the type field is a small magic number
}
