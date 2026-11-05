// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
)

// recordBoardSeen runs the proof again against the cloned store and then
// records the hash of .mtix/tasks.json as already seen in the fresh store's
// sync.sha256, so the automatic import every command runs treats the file as
// the store's own export and leaves the hub clone alone. The hub clone is
// authoritative: the file only has to be the old store's export, which the
// proof shows, and the next automatic export rewrites it from the clone. A
// board that fails the proof is never marked seen (MTIX-112.17.16.6). A
// worktree without a tasks.json has nothing to record. The file itself is
// unchanged (MTIX-112.17.16).
func recordBoardSeen(mtixDir string, verify func() (*BoardFinding, error)) error {
	path := filepath.Join(mtixDir, "tasks.json")
	body, err := os.ReadFile(path) //nolint:gosec // G304: the worktree's own board file
	absent := errors.Is(err, os.ErrNotExist)
	if err != nil && !absent {
		return fmt.Errorf("read the board file: %w", err)
	}
	if vErr := refuseBoard(verify()); vErr != nil {
		return vErr
	}
	if absent {
		return nil
	}
	if again, rErr := os.ReadFile(path); rErr != nil || !bytes.Equal(body, again) { //nolint:gosec // G304: the same board file
		return errors.Join(rErr, fmt.Errorf("tasks.json changed while enrolment checked it; run the enrolment again: %w", model.ErrConflict))
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	return writeFileAtomically(filepath.Join(mtixDir, "data", "sync.sha256"), []byte(hash))
}
