// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// twoTasks creates UQ-1 and UQ-2 in s.
func twoTasks(t *testing.T, s *Store) {
	t.Helper()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"UQ-1", "UQ-2"} {
		require.NoError(t, s.CreateNode(context.Background(), &model.Node{
			ID: id, Project: "UQ", Depth: 0, Seq: i + 1, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
}

// TestNodesUID_DuplicateInsertOrUpdate_IsRejected verifies the store itself
// enforces that two nodes never share a non-empty uid (MTIX-95.31.8),
// soft-deleted nodes included.
func TestNodesUID_DuplicateInsertOrUpdate_IsRejected(t *testing.T) {
	ctx := context.Background()
	s, err := New(filepath.Join(t.TempDir(), "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	twoTasks(t, s)
	first := uidOf(t, s, "UQ-1")
	require.NotEmpty(t, first)

	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = 'UQ-2'`, first)
	require.Error(t, err, "a second node cannot take the uid")
	assert.Contains(t, err.Error(), "UNIQUE")

	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET deleted_at = '2026-10-01T09:00:00Z' WHERE id = 'UQ-1'`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = 'UQ-2'`, first)
	require.Error(t, err, "a soft-deleted node still holds its uid")

	// Empty and NULL uids never conflict.
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id IN ('UQ-1','UQ-2')`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = NULL WHERE id = 'UQ-2'`)
	require.NoError(t, err)
}

// duplicateUIDStore builds a store file whose two nodes share a uid, the way
// a store written before the unique index can.
func duplicateUIDStore(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	twoTasks(t, s)
	ctx := context.Background()
	_, err = s.writeDB.ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx,
		`UPDATE nodes SET uid = (SELECT uid FROM nodes WHERE id = 'UQ-1') WHERE id = 'UQ-2'`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	return dbPath
}

// TestNew_ExistingDuplicateUIDs_OpensReportsAndKeepsRows verifies a store that
// already holds a shared uid opens (so the owner can run verify and the
// recovery), loses no row, logs the duplicate, and Verify names it with the
// recovery; after the recovery the next open creates the unique index.
func TestNew_ExistingDuplicateUIDs_OpensReportsAndKeepsRows(t *testing.T) {
	dbPath := duplicateUIDStore(t)
	logs := &bytes.Buffer{}
	s, err := New(dbPath, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err, "the store opens; it never fails startup on duplicates")
	dup := uidOf(t, s, "UQ-1")
	assert.Equal(t, dup, uidOf(t, s, "UQ-2"), "no uid was changed")
	assert.Contains(t, logs.String(), "duplicate_node_uids")

	res, err := s.Verify(context.Background())
	require.NoError(t, err)
	assert.False(t, res.UIDUniqueOK)
	assert.False(t, res.AllPassed, "duplicates fail the verification")
	joined := strings.Join(res.Errors, "\n")
	for _, want := range []string{"uid_unique", dup, "UQ-1", "UQ-2", "UPDATE nodes SET uid = ''"} {
		assert.Contains(t, joined, want)
	}

	_, err = s.writeDB.ExecContext(context.Background(), `UPDATE nodes SET uid = '' WHERE id = 'UQ-2'`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = New(dbPath, slog.Default()) // idempotent re-open: backfills UQ-2, indexes
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.NotEmpty(t, uidOf(t, s, "UQ-2"))
	assert.NotEqual(t, dup, uidOf(t, s, "UQ-2"))
	res, err = s.Verify(context.Background())
	require.NoError(t, err)
	assert.True(t, res.UIDUniqueOK)
	_, err = s.writeDB.ExecContext(context.Background(), `UPDATE nodes SET uid = ? WHERE id = 'UQ-2'`, dup)
	require.Error(t, err, "the unique index is now in force")
}

// TestNew_OldNonUniqueUIDIndex_UpgradedOnce verifies an existing store with
// the old non-unique index gets the unique one, and a second open changes nothing.
func TestNew_OldNonUniqueUIDIndex_UpgradedOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	twoTasks(t, s)
	_, err = s.writeDB.ExecContext(context.Background(), `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(context.Background(),
		`CREATE INDEX idx_nodes_uid ON nodes(uid) WHERE uid IS NOT NULL AND uid <> ''`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	for i := 0; i < 2; i++ {
		s, err = New(dbPath, slog.Default())
		require.NoError(t, err)
		var unique int
		require.NoError(t, s.readDB.QueryRowContext(context.Background(),
			`SELECT "unique" FROM pragma_index_list('nodes') WHERE name = 'idx_nodes_uid'`).Scan(&unique))
		assert.Equal(t, 1, unique, "open %d", i)
		require.NoError(t, s.Close())
	}
}

// TestApply_CreateNode_UIDHeldByAnotherNode_RefusedNamingHolder verifies a
// create event whose uid another local task holds is refused with the uid
// and holder named, and writes nothing, so pull can quarantine it instead of
// dropping it (MTIX-95.31.8).
func TestApply_CreateNode_UIDHeldByAnotherNode_RefusedNamingHolder(t *testing.T) {
	s, raw := applyTestStore(t)
	first := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "a"})
	require.NoError(t, applyOnce(t, s, first))

	other := makeApplyEvent(t, model.OpCreateNode, "MTIX-2", "alice", 2, &model.CreateNodePayload{Title: "b"})
	other.UID = first.EventID
	err := applyOnce(t, s, other)
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Contains(t, err.Error(), "uid "+first.EventID+" is held by local task MTIX-1")
	assert.Equal(t, 1, countNodes(t, raw))

	// A replay of the same create at the same id is still a no-op.
	replay := *first
	replay.EventID = "01a0fb06-0000-7000-8000-000000000001"
	replay.UID = first.EventID
	require.NoError(t, applyOnce(t, s, &replay))
}

// uidIndexKind returns "unique", "plain" or "" (absent) for idx_nodes_uid.
func uidIndexKind(t *testing.T, s *Store) string {
	t.Helper()
	var kind string
	err := s.readDB.QueryRowContext(context.Background(), `
		SELECT CASE WHEN "unique" = 1 THEN 'unique' ELSE 'plain' END
		FROM pragma_index_list('nodes') WHERE name = 'idx_nodes_uid'`).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	require.NoError(t, err)
	return kind
}

// TestNew_DegradedOpen_KeepsNonUniqueUIDIndex verifies a store holding
// duplicates opens with idx_nodes_uid present and non-unique, so uid lookups
// stay indexed (MTIX-95.31.8).
func TestNew_DegradedOpen_KeepsNonUniqueUIDIndex(t *testing.T) {
	for _, drop := range []bool{false, true} {
		dbPath := duplicateUIDStore(t) // leaves no uid index at all
		if !drop {
			s, err := New(dbPath, slog.Default())
			require.NoError(t, err)
			require.NoError(t, s.Close())
		}
		s, err := New(dbPath, slog.Default())
		require.NoError(t, err)
		assert.Equal(t, "plain", uidIndexKind(t, s), "index absent before open: %v", drop)
		require.NoError(t, s.Close())
	}
}

// TestEnsureUniqueUIDIndex_CreateFails_RollsBackAndStoreOpens verifies the
// swap is one transaction: a failing CREATE leaves the old plain index in
// place (the DROP is rolled back), nothing is returned as a fatal error, and
// the next call with a good statement completes the swap (MTIX-95.31.8).
func TestEnsureUniqueUIDIndex_CreateFails_RollsBackAndStoreOpens(t *testing.T) {
	ctx := context.Background()
	s, err := New(filepath.Join(t.TempDir(), "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	twoTasks(t, s)
	_, err = s.writeDB.ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx, plainUIDIndexDDL)
	require.NoError(t, err)

	require.NoError(t, s.ensureUniqueUIDIndexWith(ctx, `CREATE UNIQUE INDEX no_such_table_idx ON missing(x)`))
	assert.Equal(t, "plain", uidIndexKind(t, s), "the DROP was rolled back")

	require.NoError(t, s.ensureUniqueUIDIndex(ctx))
	assert.Equal(t, "unique", uidIndexKind(t, s))
}

// TestEnsureUniqueUIDIndex_BelowFreeSpaceFloor_OpensWithoutSwap verifies the
// swap goes through the write pre-flight: a refusal is logged and the store
// still opens with its old index.
func TestEnsureUniqueUIDIndex_BelowFreeSpaceFloor_OpensWithoutSwap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(context.Background(), `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(context.Background(), plainUIDIndexDDL)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	t.Setenv(minFreeBytesEnv, "18446744073709551615")
	logs := &bytes.Buffer{}
	s, err = New(dbPath, slog.New(slog.NewTextHandler(logs, nil)))
	require.NoError(t, err, "the store opens")
	t.Cleanup(func() { _ = s.Close() })
	assert.Equal(t, "plain", uidIndexKind(t, s))
	assert.Contains(t, logs.String(), "unique_uid_index_failed")
}

// TestNew_ConcurrentOpensOfOldStore_AllSucceed verifies opens racing to swap
// the index never fail (the swap is one transaction that re-reads the state).
func TestNew_ConcurrentOpensOfOldStore_AllSucceed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	twoTasks(t, s)
	_, err = s.writeDB.ExecContext(context.Background(), `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(context.Background(), plainUIDIndexDDL)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := New(dbPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err == nil {
				err = o.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	s, err = New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	assert.Equal(t, "unique", uidIndexKind(t, s))
}

// TestNodesUID_UniqueIndexAloneRejects_AndPlainIndexDoesNot verifies the
// guarantee comes from the index, not from application checks: the same
// duplicate UPDATE succeeds against a plain index and fails against the
// unique one (MTIX-95.31.8).
func TestNodesUID_UniqueIndexAloneRejects_AndPlainIndexDoesNot(t *testing.T) {
	ctx := context.Background()
	s, err := New(filepath.Join(t.TempDir(), "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	twoTasks(t, s)
	dup := `UPDATE nodes SET uid = (SELECT uid FROM nodes WHERE id = 'UQ-1') WHERE id = 'UQ-2'`
	_, err = s.writeDB.ExecContext(ctx, dup)
	require.Error(t, err, "unique index in force")

	_, err = s.writeDB.ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx, plainUIDIndexDDL)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx, dup)
	require.NoError(t, err, "a plain index lets the duplicate in; only the unique index rejects it")
}
