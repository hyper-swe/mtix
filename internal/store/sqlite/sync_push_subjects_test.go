// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The reads behind the subtree rule of held task creations (MTIX-95.12,
// review r5 S1/S2): the task an event is about, by uid, and the tasks a
// number meant.

// eventOf returns the id of the latest local event of op for node.
func eventOf(t *testing.T, s *sqlite.Store, node, op string) string {
	t.Helper()
	var id string
	require.NoError(t, s.QueryRow(context.Background(), `
		SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = ?
		ORDER BY lamport_clock DESC LIMIT 1`, node, op).Scan(&id))
	return id
}

// TestPushSubjects_TaskFoundByUID: an event's subject is the node whose uid
// is the event's uid, under its current number after a local renumber and
// after a soft delete; an event without a uid has no task; an unknown id is
// left out.
func TestPushSubjects_TaskFoundByUID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "held", now)))
	create := eventOf(t, s, "PROJ-1", "create_node")
	require.NoError(t, s.RenumberSubtree(ctx, "PROJ-1", 5))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-5", false, "tester"))
	deleted := eventOf(t, s, "PROJ-5", "delete")
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = NULL WHERE event_id = ?`, deleted)
	require.NoError(t, err)

	got, err := s.PushSubjects(ctx, []string{create, deleted, "unknown"})
	require.NoError(t, err)
	require.Equal(t, create, got[create].UID)
	require.Equal(t, "PROJ-5", got[create].CurrentNodeID, "found by uid under the new number, soft-deleted")
	require.Equal(t, sqlite.PushSubject{}, got[deleted], "no uid, no task")
	require.NotContains(t, got, "unknown")
	none, err := s.PushSubjects(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

// TestDataVersion_ChangesOnlyForOtherConnections: the data version read on
// the write connection stays the same after this store's own writes and
// changes once another connection to the same database commits.
func TestDataVersion_ChangesOnlyForOtherConnections(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	before, err := s.DataVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "own write", now)))
	own, err := s.DataVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, own, "this store's own writes do not count")

	other, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, other.RenumberSubtree(ctx, "PROJ-1", 5))
	require.NoError(t, other.Close())
	after, err := s.DataVersion(ctx)
	require.NoError(t, err)
	require.NotEqual(t, before, after, "a commit by another connection counts")
}
