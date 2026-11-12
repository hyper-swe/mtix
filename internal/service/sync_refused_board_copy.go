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

// refusedBoardsDir is where mtix sync --fix keeps a copy of a refused
// .mtix/tasks.json before it rewrites the file from the store (MTIX-112.17.16.16).
// It is under the data directory, which is local bookkeeping and not tracked.
const refusedBoardsDir = "refused-boards"

// refusedBoardCopyHint names the copy in the refusal message, the pending
// line and the docs: the real name carries the UTC time of the fix.
const refusedBoardCopyHint = ".mtix/data/refused-boards/tasks-<UTC time>.json"

// refusedBoardLayout is the time layout of a copy's name.
const refusedBoardLayout = "20060102T150405Z"

// keepRefusedBoard copies the .mtix/tasks.json on disk to a timestamped file
// under data/refused-boards when it differs from what mtix last wrote or
// imported (its hash is not sync.sha256, or that record is missing), so
// mtix sync --fix never destroys edits the file carried
// (MTIX-112.17.16.16, MTIX-112.17.16.42). The rule does not depend on the
// refusal record: a refused file, one edited after the refusal, and a record
// that cannot be read are all kept. The copy is private (0600). It never
// replaces an existing copy: a taken name gets a numeric suffix. It prints
// the copy's path and returns it, or returns "" when the file is missing or
// equals the last export.
func (s *SyncService) keepRefusedBoard(mtixDir string) (string, error) {
	// #nosec G703 -- caller selects the local project directory; tasks.json is the fixed board filename.
	data, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the board to keep a copy: %w", err)
	}
	// #nosec G703 -- caller selects the local project directory; data/sync.sha256 is fixed local bookkeeping.
	stored, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	if err == nil && string(stored) == fmt.Sprintf("%x", sha256.Sum256(data)) {
		return "", nil // the file is what mtix last wrote or imported: nothing to lose
	}
	dir := filepath.Join(mtixDir, "data", refusedBoardsDir)
	if mkErr := os.MkdirAll(dir, 0o750); mkErr != nil {
		return "", fmt.Errorf("create %s: %w", dir, mkErr)
	}
	path, err := writeNewFile(dir, "tasks-"+s.clock().UTC().Format(refusedBoardLayout), ".json", data)
	if err != nil {
		return "", fmt.Errorf("keep a copy of the refused board: %w", err)
	}
	s.mu.Lock()
	fmt.Fprintf(s.notices, "mtix: the .mtix/tasks.json that differs from the last export was kept at %s before it is rewritten from the store.\n", path)
	s.mu.Unlock()
	return path, nil
}

// writeNewFile creates dir/stem+ext holding data, or dir/stem-N+ext for the
// first free N from 2, and fails rather than replace a file that exists.
func writeNewFile(dir, stem, ext string, data []byte) (string, error) {
	for n := 1; n < 1000; n++ {
		name := stem + ext
		if n > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, n, ext)
		}
		path := filepath.Join(dir, name)
		// #nosec G304 -- dir is the local data directory and name is built here, not received.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", werr
		}
		return path, nil
	}
	return "", fmt.Errorf("no free name for %s in %s", stem, dir)
}
