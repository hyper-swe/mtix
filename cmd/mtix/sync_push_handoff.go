// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

const pushHandoffPassLimit = 3

type pushHandoffOptions struct {
	mtixDir string
	force   bool
}

// pushWithHandoff checks for committed arrivals only AFTER releasing ownership.
// Within the pass budget, a contender commits before Acquire: either this check
// sees its event or it can acquire the unlocked lock itself. At the cap, arrivals
// remain pending for the next push. A non-pusher holder does not drain events.
// pass is instance-local and always runs fresh native reads after acquisition.
func pushWithHandoff(ctx context.Context, stderr io.Writer, store *sqlite.Store,
	opts pushHandoffOptions, pass func() (bool, error),
) error {
	if opts.force {
		_, err := pass()
		return err
	}
	for n := 0; n < pushHandoffPassLimit; n++ {
		lock, err := pushlock.Acquire(opts.mtixDir)
		if errors.Is(err, pushlock.ErrLockHeld) {
			fmt.Fprintln(stderr, "mtix sync push: push lock is held; skipping (queued work remains pending; retry the next mtix sync push)")
			return nil
		}
		if err != nil {
			return wrapSyncErr(stderr, "pushlock", err)
		}
		drained, err := pushOwnedPass(stderr, lock, pass)
		if err != nil || !drained || n == pushHandoffPassLimit-1 {
			return err
		}
		events, err := readPendingBatch(ctx, store, 1)
		if err != nil {
			return wrapSyncErr(stderr, "handoff queue", err)
		}
		if len(events) == 0 {
			return nil
		}
	}
	return nil
}

func pushOwnedPass(stderr io.Writer, lock *pushlock.PushLock, pass func() (bool, error)) (drained bool, err error) {
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			err = errors.Join(err, wrapSyncErr(stderr, "release pushlock", releaseErr))
		}
	}()
	return pass()
}
