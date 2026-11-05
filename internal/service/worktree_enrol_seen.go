// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// recordBoardSeen records the hash of .mtix/tasks.json as already seen in the
// fresh store's sync.sha256, so the automatic import every command runs treats
// the file as the store's own export and leaves the hub clone alone. A worktree
// without a tasks.json has nothing to record. The file itself is unchanged; the
// next automatic export rewrites it from the cloned store (MTIX-112.17.16).
func recordBoardSeen(mtixDir string) error {
	body, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json")) //nolint:gosec // G304: the worktree's own board file
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the board file: %w", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	return writeFileAtomically(filepath.Join(mtixDir, "data", "sync.sha256"), []byte(hash))
}
