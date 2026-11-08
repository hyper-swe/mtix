// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// plainFirstDone rewrites the first done transition of node into a plain 0.5.4
// event (no closed_at) whose wall clock is at, and returns the others untouched.
func plainFirstDone(t *testing.T, events []*model.SyncEvent, node string, at time.Time) {
	t.Helper()
	for _, e := range events {
		if e.OpType != model.OpTransitionStatus || e.NodeID != node {
			continue
		}
		var p map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		if string(p["to"]) != `"done"` {
			continue
		}
		delete(p, "closed_at")
		var err error
		e.Payload, err = json.Marshal(p)
		require.NoError(t, err)
		e.WallClockTS = at.UnixMilli()
		return
	}
	require.FailNow(t, "no done event")
}

// skewStore moves the stored closed_at of TEST-4 one second after the value the
// last workflow event carried, as a plain done's second clock read would.
func skewStore(ctx context.Context, t *testing.T, st *sqlite.Store) time.Time {
	t.Helper()
	var stored string
	require.NoError(t, st.WriteDB().QueryRowContext(ctx, `SELECT closed_at FROM nodes WHERE id = 'TEST-4'`).Scan(&stored))
	at, err := time.Parse(time.RFC3339, stored)
	require.NoError(t, err)
	_, err = st.WriteDB().ExecContext(ctx, `UPDATE nodes SET closed_at = ? WHERE id = 'TEST-4'`, at.Add(time.Second).UTC().Format(time.RFC3339))
	require.NoError(t, err)
	return at
}

// TestEnrolWorktree_RealPath_DoneStampWinnerIsModern_StillRefuses
// (MTIX-112.17.16.19.1, F1): the excuse is about the event that set the clone's
// closed_at. When a later modern event wins and carries closed_at, an earlier
// plain done of the same second excuses nothing.
func TestEnrolWorktree_RealPath_DoneStampWinnerIsModern_StillRefuses(t *testing.T) {
	tests := []struct {
		name  string
		later func(ctx context.Context, t *testing.T, st *sqlite.Store)
	}{
		{"reopen then a modern done", func(ctx context.Context, t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.TransitionStatus(ctx, "TEST-4", model.StatusOpen, "reopen", "lane-a"))
			require.NoError(t, st.TransitionStatus(ctx, "TEST-4", model.StatusInProgress, "again", "lane-a"))
			require.NoError(t, st.TransitionStatus(ctx, "TEST-4", model.StatusDone, "finished again", "lane-a"))
		}},
		{"a modern invalidate", func(ctx context.Context, t *testing.T, st *sqlite.Store) {
			require.NoError(t, st.TransitionStatus(ctx, "TEST-4", model.StatusInvalidated, "parent changed", "lane-a"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var carried time.Time
			root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) {
				pushedWork(ctx, t, st)
				tt.later(ctx, t, st)
				carried = skewStore(ctx, t, st)
			})
			require.NoError(t, writer.Close())
			plainFirstDone(t, hubEvents, "TEST-4", carried.Add(500*time.Millisecond))
			svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

			_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

			var refusal *service.WorktreeBoardError
			require.ErrorAs(t, err, &refusal, "recorded=%v", *recorded)
			assert.Contains(t, refusal.Diff["TEST-4"], "closed_at")
			assert.False(t, *recorded)
		})
	}
}

// F2: the full-clone, aside and snapshot paths pass the clone's evidence on.

// snapshot path: a never-pulled store with no journal row is compared with the
// full clone, and a plain done one second apart still enrols.
func TestEnrolWorktree_RealPath_SnapshotStore_PlainDoneSkew_Enrols(t *testing.T) {
	ctx := context.Background()
	var carried time.Time
	root, writer, hubEvents := snapshotWorktreeOf(t)
	carried = skewStore(ctx, t, writer)
	plainFirstDone(t, hubEvents, "TEST-4", carried.Add(500*time.Millisecond))
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	require.NoError(t, err)
	assert.True(t, *recorded)
}

// full path: the bounded clone lacks TEST-4 (its journal rows are gone), so the
// finding is confirmed against the full clone; the held row differs only by the
// excused skew and must not list closed_at.
func TestBoardHubFinding_HeldRowWithPlainDoneSkew_ListsNoField(t *testing.T) {
	ctx := context.Background()
	var carried time.Time
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	carried = skewStore(ctx, t, writer)
	plainFirstDone(t, hubEvents, "TEST-4", carried.Add(500*time.Millisecond))
	_, err := writer.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE node_id = 'TEST-4'`)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	hub := hubOf(hubEvents)

	var finding *service.BoardFinding
	require.NoError(t, withScratch(slog.Default(), root, func(scratch string) error {
		return withBoardStore(slog.Default(), root, worktreeDataDir(root), func(st *sqlite.Store) (e error) {
			finding, e = boardHubFinding(ctx, slog.Default(), root, scratch, st, hub)
			return e
		})
	}))

	require.NotNil(t, finding, "the bounded clone lacks TEST-4")
	assert.Contains(t, finding.StoreOnlyRows(), "TEST-4")
	assert.Empty(t, finding.HeldFields()["TEST-4"], "the hub holds TEST-4 with the same values but the excused stamp")
}

// aside path: after the swap, the old store is compared with the real clone for
// the entities its own journal touched; an excused stamp is not lost work.
func TestLostLocalValues_PlainDoneSkew_IsNotLost(t *testing.T) {
	ctx := context.Background()
	var carried time.Time
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	carried = skewStore(ctx, t, writer)
	plainFirstDone(t, hubEvents, "TEST-4", carried.Add(500*time.Millisecond))
	require.NoError(t, writer.Close())
	aside := filepath.Join(root, ".mtix", "aside-store")
	require.NoError(t, os.Rename(worktreeDataDir(root), aside))
	hub := hubOf(hubEvents)
	require.NoError(t, withBoardStore(slog.Default(), root, worktreeDataDir(root), func(clone *sqlite.Store) error {
		return seedFreshStore(ctx, clone, io.Discard, boundedHub{cursorPuller: hub, bound: appliedBound{snapshot: true}})
	}))

	lost, err := lostLocalValues(ctx, slog.Default(), root, aside)

	require.NoError(t, err)
	assert.Nil(t, lost)
}
