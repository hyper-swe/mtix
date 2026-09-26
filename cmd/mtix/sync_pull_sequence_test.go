// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// The next `mtix create` after `mtix sync clone`, or after a pull of k
// teammate creates, succeeds at the first attempt with the next free number
// (MTIX-95.38). Before, applying a pulled or cloned create_node did not
// advance the local sequence counter: each create then failed with
// "already exists" once for every pulled task above the counter, and each
// failed create moved the counter on by one, so the k+1-th attempt
// succeeded. The fake hub is fakeLateHub (sync_pull_sweep_test.go).

// rootCounter returns the peer store's TEST root sequence counter, 0 when
// it has no row.
func rootCounter(t *testing.T) int {
	t.Helper()
	return countTestRows(t, `SELECT COALESCE((SELECT value FROM sequences WHERE key = 'TEST:'), 0)`)
}

// createUntilSuccess runs `mtix create` for a root task titled title until
// it succeeds, at most limit times. It returns how many attempts failed
// with ErrAlreadyExists and the id the successful attempt created, and
// logs the counter after each failed attempt.
func createUntilSuccess(t *testing.T, title string, limit int) (int, string) {
	t.Helper()
	for failures := 0; failures < limit; failures++ {
		err := runCreate(title, "", "", 3, "", "", "", "", "")
		if err == nil {
			var id string
			require.NoError(t, app.store.QueryRow(context.Background(),
				`SELECT id FROM nodes WHERE title = ?`, title).Scan(&id))
			return failures, id
		}
		require.ErrorIs(t, err, model.ErrAlreadyExists, "attempt %d", failures+1)
		t.Logf("create attempt %d failed (%v); the TEST root counter is now %d", failures+1, err, rootCounter(t))
	}
	t.Fatalf("no create succeeded in %d attempts", limit)
	return limit, ""
}

// TestCheckThenClone_KTeammateCreates_NextCreateSucceedsFirstAttempt: a
// clone of k creates into an empty store leaves the root counter at k, and
// the next create succeeds at the first attempt as TEST-(k+1).
func TestCheckThenClone_KTeammateCreates_NextCreateSucceedsFirstAttempt(t *testing.T) {
	for _, k := range []int{1, 5, 20} {
		t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
			initTestApp(t)
			lamports := make([]int64, k)
			for i := range lamports {
				lamports[i] = int64(i + 1)
			}
			hub := &fakeLateHub{pullEvents: remoteCreates(t, lamports...)}
			_, _, stage, err := checkThenClone(context.Background(), &bytes.Buffer{}, hub, transport.PullCursor{}, 7)
			require.NoError(t, err, stage)
			afterClone := rootCounter(t)

			failures, id := createUntilSuccess(t, "local after clone", k+2)

			require.Zero(t, failures, "failed creates before the first success")
			require.Equal(t, fmt.Sprintf("TEST-%d", k+1), id)
			require.Equal(t, k, afterClone, "the clone advances the counter to the highest cloned number")
			require.Equal(t, k+1, rootCounter(t), "no number is skipped")
		})
	}
}

// TestPullLoop_KTeammateCreates_NextCreateSucceedsFirstAttempt: a store
// that created TEST-1 and TEST-2 pulls a teammate's TEST-3 .. TEST-(k+2);
// the pull leaves the root counter at k+2, and the next create succeeds at
// the first attempt as TEST-(k+3).
func TestPullLoop_KTeammateCreates_NextCreateSucceedsFirstAttempt(t *testing.T) {
	for _, k := range []int{1, 5, 20} {
		t.Run(fmt.Sprintf("k=%d", k), func(t *testing.T) {
			initTestApp(t)
			for _, title := range []string{"local one", "local two"} {
				require.NoError(t, runCreate(title, "", "", 3, "", "", "", "", ""))
			}
			events := make([]*model.SyncEvent, k)
			for i := range events {
				events[i] = remoteCreateAt(t, fmt.Sprintf("TEST-%d", i+3), int64(100+i))
			}
			hub := &fakeLateHub{pullEvents: events}
			applied, _, err := pullLoop(context.Background(), testIngest(nil), hub, app.store, transport.PullCursor{}, 7)
			require.NoError(t, err)
			require.Equal(t, k, applied)
			afterPull := rootCounter(t)

			failures, id := createUntilSuccess(t, "local after pull", k+2)

			require.Zero(t, failures, "failed creates before the first success")
			require.Equal(t, fmt.Sprintf("TEST-%d", k+3), id)
			require.Equal(t, k+2, afterPull, "the pull advances the counter to the highest pulled number")
			require.Equal(t, k+3, rootCounter(t), "no number is skipped")
		})
	}
}
