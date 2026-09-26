// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.9 (FR-7.8, folding in MTIX-95.31.10): a task that
// reaches the store without a uid (from a board written before uids were
// shared) is given a backfill uid, a UUIDv8 with a fixed marker, inside the
// import's transaction, and a store that still holds tasks without a uid
// backfills them when it opens. Boards the store then writes carry a uid
// for every node, the uid survives export and import unchanged, and the
// identity rule treats a marked backfill uid as one assigned later, so two
// clones that each backfilled the same task (the same creation time) keep
// one task. Written red-first against round 1, which left such uids empty.
package sqlite_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// importBackfillUID returns a marked backfill uid, as an import or an open
// gives a task that has none (MTIX-95.31.9).
func importBackfillUID(t *testing.T) string {
	t.Helper()
	uid, err := model.NewBackfillUID()
	require.NoError(t, err)
	return uid
}

// uidlessBoard returns a board of REC-1 and REC-2 without uids and REC-3
// under uid, created at sameIDTime.
func uidlessBoard(t *testing.T, uid string) *sqlite.ExportData {
	t.Helper()
	node := func(id string, seq int, uid string) sqlite.TestExportNode {
		return sqlite.TestExportNode{ID: id, Project: "REC", Seq: seq, Title: "Task " + id, ContentHash: "h-" + id,
			UID: uid, CreatedAt: sameIDTime, UpdatedAt: sameIDTime}
	}
	return reconcileExport(t, "REC", node("REC-1", 1, ""), node("REC-2", 2, ""), node("REC-3", 3, uid))
}

// TestImport_NodeWithoutUID_GetsMarkedBackfillUID verifies every node an
// import inserts without a uid (replace, which the automatic import runs,
// or merge) is stored with a marked backfill uid, a node with a uid keeps
// it, the store's export carries a uid for every node, and the uids survive
// a further export and import unchanged.
func TestImport_NodeWithoutUID_GetsMarkedBackfillUID(t *testing.T) {
	tests := []struct {
		name  string
		mode  sqlite.ImportMode
		local bool // the store already holds REC-9
	}{
		{"replace", sqlite.ImportModeReplace, false},
		{"merge into an empty store", sqlite.ImportModeMerge, false},
		{"merge beside a local task", sqlite.ImportModeMerge, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			if tt.local {
				createSameIDTask(t, s, "REC-9", "", 9, taskUID(t), "Local task")
			}
			kept := taskUID(t)

			_, _, err := s.ImportReconcile(ctx, uidlessBoard(t, kept), sqlite.ImportReconcileOptions{
				Mode: tt.mode, Force: true, Confirm: true,
			})
			require.NoError(t, err)
			for _, id := range []string{"REC-1", "REC-2"} {
				assert.True(t, model.IsBackfillUID(uidAtID(t, s, id)), "node %s gets a marked backfill uid", id)
			}
			assert.Equal(t, kept, uidAtID(t, s, "REC-3"), "a node with a uid keeps it")
			board := exportOf(t, s)
			for i := range board.Nodes {
				assert.NotEmpty(t, board.Nodes[i].UID, "the board carries a uid for node %s", board.Nodes[i].ID)
			}

			other := newTestStore(t)
			_, _, err = other.ImportReconcile(ctx, board, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeReplace})
			require.NoError(t, err)
			for i := range board.Nodes {
				assert.Equal(t, board.Nodes[i].UID, uidAtID(t, other, board.Nodes[i].ID), "the uid survives unchanged")
			}
		})
	}
}

// TestNew_NodesWithoutUID_BackfilledOnOpenOnce verifies a store that holds
// nodes without a uid (NULL or empty) gives each a marked backfill uid when
// it opens and logs how many, keeps the uids it already has, and changes
// nothing on the next open.
func TestNew_NodesWithoutUID_BackfilledOnOpenOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	open := func() *sqlite.Store {
		s, err := sqlite.New(dir, logger)
		require.NoError(t, err)
		return s
	}
	s := open()
	createSameIDTask(t, s, "REC-1", "", 1, taskUID(t), "Task one")
	createSameIDTask(t, s, "REC-2", "", 2, taskUID(t), "Task two")
	kept := createSameIDTask(t, s, "REC-3", "", 3, taskUID(t), "Task three")
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id = 'REC-1'`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id = 'REC-2'`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	logs.Reset()

	s = open()
	first := map[string]string{"REC-1": uidAtID(t, s, "REC-1"), "REC-2": uidAtID(t, s, "REC-2")}
	for id, uid := range first {
		assert.True(t, model.IsBackfillUID(uid), "node %s is backfilled on open", id)
	}
	assert.Equal(t, kept, uidAtID(t, s, "REC-3"))
	assert.Contains(t, logs.String(), "backfill_uid_on_open")
	assert.Contains(t, logs.String(), "count=2")
	require.NoError(t, s.Close())
	logs.Reset()

	s = open()
	t.Cleanup(func() { _ = s.Close() })
	for id, uid := range first {
		assert.Equal(t, uid, uidAtID(t, s, id), "the next open changes nothing")
	}
	assert.NotContains(t, logs.String(), "backfill_uid_on_open")
}

// TestBackfillUIDs_NoCreateEvent_MintsMarkedUID verifies the upgrade
// backfill (BackfillUIDs) gives a node without a create event a marked
// backfill uid too, never a UUIDv7 that the identity rule could read as
// minted at creation.
func TestBackfillUIDs_NoCreateEvent_MintsMarkedUID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createSameIDTask(t, s, "REC-1", "", 1, taskUID(t), "Task one")
	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = NULL`)
	require.NoError(t, err)

	require.NoError(t, s.BackfillUIDs(ctx))
	uid := uidAtID(t, s, "REC-1")
	assert.True(t, model.IsBackfillUID(uid))
	assert.NotEqual(t, uuid.Version(7), uuid.MustParse(uid).Version())
}

// TestNew_UIDBackfillFails_StoreStillOpensAndLogs verifies a backfill on
// open that cannot write (here a trigger refuses it) does not keep the
// store from opening: the failure is logged, the node keeps no uid, and the
// next open tries again (MTIX-95.31.9).
func TestNew_UIDBackfillFails_StoreStillOpensAndLogs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	s, err := sqlite.New(dir, logger)
	require.NoError(t, err)
	createSameIDTask(t, s, "REC-1", "", 1, taskUID(t), "Task one")
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id = 'REC-1'`)
	require.NoError(t, err)
	// Every write of a uid fails from here on.
	_, err = s.WriteDB().ExecContext(ctx,
		`CREATE TRIGGER refuse_uid BEFORE UPDATE OF uid ON nodes BEGIN SELECT RAISE(ABORT, 'uid refused'); END`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	logs.Reset()

	s, err = sqlite.New(dir, logger)
	require.NoError(t, err, "the store opens although the backfill failed")
	t.Cleanup(func() { _ = s.Close() })
	assert.Contains(t, logs.String(), "backfill_uid_on_open_failed")
	assert.Contains(t, logs.String(), "uid refused")
	assert.Empty(t, uidAtID(t, s, "REC-1"))
}
