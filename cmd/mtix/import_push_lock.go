// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// holdPushForAdoption takes the push lock for a merge import that adopts a
// uid (MTIX-95.31.16): the adoption rewrites the task's pending events, and
// a push in flight would then mark as pushed the events it sent under the
// old uid. The lock is the one `mtix sync push` holds for the whole push
// (FR-18.18). While a push runs the import is refused and writes nothing,
// so run it again once the push has ended.
func holdPushForAdoption() (func(), error) {
	if app.mtixDir == "" {
		return func() {}, nil
	}
	lock, err := pushlock.Acquire(app.mtixDir)
	if errors.Is(err, pushlock.ErrLockHeld) {
		return nil, fmt.Errorf("a push is running and the merge would adopt a uid for a task with pending "+
			"changes; run the import again when the push has ended: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("take the push lock for the uid adoption: %w", err)
	}
	return func() {
		if relErr := lock.Release(); relErr != nil {
			fmt.Fprintf(os.Stderr, "warning: release the push lock: %v\n", relErr)
		}
	}, nil
}
