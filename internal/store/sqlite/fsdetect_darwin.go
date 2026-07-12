// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package sqlite

import (
	"fmt"
	"syscall"
)

// detectFS classifies the filesystem holding dir using the statfs
// f_fstypename string (e.g. "apfs", "nfs", "smbfs", "macfuse").
func detectFS(dir string) (string, fsClass, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", fsLocal, fmt.Errorf("statfs %s: %w", dir, err)
	}
	name := fstypenameToString(st.Fstypename[:])
	return name, classifyFSName(name), nil
}

// fstypenameToString reads the NUL-terminated f_fstypename byte array.
func fstypenameToString(b []int8) string {
	buf := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c)) //nolint:gosec // f_fstypename is ASCII; int8->byte is a safe reinterpretation
	}
	return string(buf)
}
