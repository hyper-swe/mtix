// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// storeWithUIDlessTask creates a store at dbPath holding REC-1 without a
// uid, runs setup on it, and closes it.
func storeWithUIDlessTask(t *testing.T, dbPath string, setup string) {
	t.Helper()
	ctx := context.Background()
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Task one",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-1", CreatedAt: created, UpdatedAt: created,
	}))
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id = 'REC-1'`)
	require.NoError(t, err)
	if setup != "" {
		_, err = s.writeDB.ExecContext(ctx, setup)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
}

// uidOf returns node id's uid in s ("" for none).
func uidOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, s.readDB.QueryRowContext(context.Background(),
		`SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, id).Scan(&uid))
	return uid
}

// TestNew_BackfillBelowTheFreeSpaceFloor_SkippedAndOpens verifies the
// backfill on open is a write like any other (MTIX-95.31.9, NFR-2.8): below
// the free-space floor its pre-flight refuses it, so it writes nothing, the
// refusal is logged, and the store still opens.
func TestNew_BackfillBelowTheFreeSpaceFloor_SkippedAndOpens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	storeWithUIDlessTask(t, dbPath, "")
	t.Setenv(minFreeBytesEnv, "18446744073709551615") // above any volume's free space
	logs := &bytes.Buffer{}

	s, err := New(dbPath, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err, "the store opens")
	t.Cleanup(func() { _ = s.Close() })
	assert.Empty(t, uidOf(t, s, "REC-1"), "nothing was written")
	assert.Contains(t, logs.String(), "backfill_uid_on_open_failed")
	assert.Contains(t, logs.String(), "refusing write")
	assert.Nil(t, s.failStopCause(), "a pre-flight refusal does not latch fail-stop")
}

// TestNew_BackfillHitsAFullDisk_LatchesFailStop verifies a fatal storage
// error in the backfill on open (an injected SQLITE_FULL) latches
// fail-stop like any write (MTIX-95.31.9, NFR-2.8), while the store still
// opens: every later write is refused.
func TestNew_BackfillHitsAFullDisk_LatchesFailStop(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	storeWithUIDlessTask(t, dbPath,
		`CREATE TRIGGER full_disk BEFORE UPDATE OF uid ON nodes BEGIN SELECT RAISE(ABORT, 'database or disk is full'); END`)

	s, err := New(dbPath, slog.Default())
	require.NoError(t, err, "the store opens")
	t.Cleanup(func() { _ = s.Close() })
	require.Error(t, s.failStopCause(), "the full disk latched fail-stop")
	err = s.WithTx(context.Background(), func(*sql.Tx) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail-stop")
	assert.Empty(t, uidOf(t, s, "REC-1"))
}

// TestSetBackfillUIDs_TaskGotAUIDAfterTheScan_KeepsItAndIsNotCounted
// verifies the guarded write of the backfill (MTIX-95.31.9): of the nodes a
// scan found without a uid, one that another process gave a uid after the
// scan keeps that uid and is not counted; the others get a backfill uid.
func TestSetBackfillUIDs_TaskGotAUIDAfterTheScan_KeepsItAndIsNotCounted(t *testing.T) {
	ctx := context.Background()
	s, err := New(filepath.Join(t.TempDir(), "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for seq, id := range []string{"REC-1", "REC-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "REC", Depth: 0, Seq: seq + 1, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id = 'REC-1'`)
	require.NoError(t, err)
	const assigned = "01a0d56f-0000-7000-8000-00000000c010" // REC-2's uid, assigned after the scan
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = 'REC-2'`, assigned)
	require.NoError(t, err)

	var n int
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		var setErr error
		n, setErr = setBackfillUIDs(ctx, tx, []string{"REC-1", "REC-2"}) // as the scan found them
		return setErr
	}))
	assert.Equal(t, 1, n, "only REC-1 is counted")
	assert.True(t, model.IsBackfillUID(uidOf(t, s, "REC-1")))
	assert.Equal(t, assigned, uidOf(t, s, "REC-2"), "the uid assigned after the scan stays")
}

// TestMintMissingUIDs_CountsTheTasksItGaveAUID verifies the backfill
// returns how many tasks it gave a uid, counted from the rows it changed,
// and gives none the second time.
func TestMintMissingUIDs_CountsTheTasksItGaveAUID(t *testing.T) {
	ctx := context.Background()
	s, err := New(filepath.Join(t.TempDir(), "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for seq, id := range []string{"REC-1", "REC-2", "REC-3"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "REC", Depth: 0, Seq: seq + 1, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id IN ('REC-1', 'REC-2')`)
	require.NoError(t, err)

	n, err := s.mintMissingUIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	n, err = s.mintMissingUIDs(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to give a uid")
}

// TestNew_PreV3StoreBelowTheFreeSpaceFloor_OpensReadsAndMintsLater verifies
// the pre-v3 migration only runs the deterministic backfill from create
// events, as before (MTIX-95.31.9, round 4): below the free-space floor a
// pre-v3 store whose task has no create event still opens and reads
// (NFR-2.8), nothing is minted, and a later open above the floor gives the
// task a backfill uid.
func TestNew_PreV3StoreBelowTheFreeSpaceFloor_OpensReadsAndMintsLater(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	storeWithUIDlessTask(t, dbPath, "")
	raw, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	_, err = raw.writeDB.ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)
	_, err = raw.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = NULL`)
	require.NoError(t, err)
	_, err = raw.writeDB.ExecContext(ctx, `UPDATE meta SET value = '2' WHERE key = 'schema_version'`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	t.Setenv(minFreeBytesEnv, "18446744073709551615") // above any volume's free space
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err, "a pre-v3 store below the floor opens")
	node, err := s.GetNode(ctx, "REC-1")
	require.NoError(t, err, "reads work")
	assert.Equal(t, "Task one", node.Title)
	assert.Empty(t, uidOf(t, s, "REC-1"), "nothing is minted below the floor")
	require.NoError(t, s.Close())

	t.Setenv(minFreeBytesEnv, "0")
	s, err = New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.True(t, model.IsBackfillUID(uidOf(t, s, "REC-1")), "a later open above the floor mints")
}

// TestNew_IntegrityChecksSkipped_BackfillSkippedAndLogged verifies an open
// with MTIX_SKIP_INTEGRITY_CHECK=1, the escape hatch for reading a damaged
// store, writes no backfill uid and says so (MTIX-95.31.9, round 4).
func TestNew_IntegrityChecksSkipped_BackfillSkippedAndLogged(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	storeWithUIDlessTask(t, dbPath, "")
	t.Setenv(skipIntegrityCheckEnv, "1")
	logs := &bytes.Buffer{}

	s, err := New(dbPath, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.Empty(t, uidOf(t, s, "REC-1"), "nothing is written")
	assert.Contains(t, logs.String(), "backfill_uid_on_open_skipped")
}
