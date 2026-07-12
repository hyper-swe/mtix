// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sqlite

import (
	"fmt"
	"syscall"
)

// detectFS classifies the filesystem holding dir using the statfs f_type
// magic (linux/magic.h), mapped to a name via linuxFSMagicName.
func detectFS(dir string) (string, fsClass, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return "", fsLocal, fmt.Errorf("statfs %s: %w", dir, err)
	}
	name := linuxFSMagicName(int64(st.Type))
	return name, classifyFSName(name), nil
}
