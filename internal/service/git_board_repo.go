// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Only supported ordinary repository metadata is inspected, without Git probes.
package service

import (
	"os"
	"path/filepath"
)

func boardRepository(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", gitBoardError("invalid checkout path", nil)
	}
	dir, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", gitBoardError("checkout unavailable", nil)
	}
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Lstat(gitDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", gitBoardError("unsupported Git metadata", nil)
	}
	for _, name := range []string{"commondir", "info/grafts", "shallow", "objects/info/alternates"} {
		_, err = os.Lstat(filepath.Join(gitDir, filepath.FromSlash(name)))
		if err == nil || !os.IsNotExist(err) {
			return "", gitBoardError("unsupported ancestry metadata", nil)
		}
	}
	// A symlinked info directory could hide metadata outside the inspected root.
	info, err = os.Lstat(filepath.Join(gitDir, "info"))
	if err != nil && !os.IsNotExist(err) {
		return "", gitBoardError("Git metadata unavailable", nil)
	}
	if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return "", gitBoardError("unsupported Git metadata", nil)
	}
	return dir, nil
}
