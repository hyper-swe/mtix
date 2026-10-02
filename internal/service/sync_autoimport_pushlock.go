// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// holdPushForImport takes the push lock for an automatic import
// (MTIX-95.31.16, FR-18.18): the replace the import runs can give a task the
// file's uid and then moves the task's pending events with it, which a push
// in flight must not see half done. It returns the release to defer, or
// skip when the import must not run now: while a push runs it prints one
// line and skips, so the user's command never fails and the pulled board,
// whose hash is not recorded, is imported by the next command. An error
// taking the lock (not "held") is logged and the import runs without it.
func (s *SyncService) holdPushForImport(mtixDir string) (release func(), skip bool) {
	lock, err := pushlock.Acquire(mtixDir)
	if errors.Is(err, pushlock.ErrLockHeld) {
		fmt.Fprint(s.notices, "mtix: a push is running; the changed .mtix/tasks.json is imported by the next command\n")
		return nil, true
	}
	if err != nil {
		// The lock file cannot be opened (a read-only data directory): the
		// import must not be lost for that, so it runs without the lock.
		s.logger.Warn("could not take the push lock, importing without it", "error", err)
		return func() {}, false
	}
	return func() {
		if relErr := lock.Release(); relErr != nil {
			s.logger.Warn("could not release the push lock", "error", relErr)
		}
	}, false
}
