// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// TDD RED suite for MTIX-30.1 (ADR-003 §2, §7 Phase 0, §13): a node's
// internal UID is its create_node event id — globally unique by the
// event-log PK and identical on every replica by construction. These
// tests are written before the implementation and MUST fail (RED) until
// the UID foundation lands. Scenario coverage per the ticket: happy +
// corner + edge, not best-case only.
package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// createEventIDFor returns the create_node event id recorded for a node,
// read straight from the local event log — the value the node's UID must
// equal.
func createEventIDFor(t *testing.T, s *sqlite.Store, nodeID string) string {
	t.Helper()
	var eid string
	err := s.ReadDB().QueryRowContext(context.Background(),
		`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = 'create_node'`,
		nodeID).Scan(&eid)
	require.NoError(t, err, "no create_node event for %s", nodeID)
	return eid
}

func mkNode(id, parent, project, title string) *model.Node {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	n := &model.Node{
		ID: id, ParentID: parent, Project: project, Title: title,
		NodeType: model.NodeTypeForDepth(0), Priority: model.PriorityMedium,
		Status: model.StatusOpen, Weight: 1.0, Creator: "test",
		CreatedAt: now, UpdatedAt: now,
	}
	n.ContentHash = n.ComputeHash()
	return n
}

// TestCreateNode_UID_EqualsCreateEventID — the core contract: on create,
// the node's UID is its own create_node event id.
func TestCreateNode_UID_EqualsCreateEventID(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	n := mkNode("PRJX-1", "", "PRJX", "root")
	require.NoError(t, s.CreateNode(ctx, n))

	got, err := s.GetNode(ctx, "PRJX-1")
	require.NoError(t, err)
	assert.NotEmpty(t, got.UID, "created node must carry a UID")
	assert.Equal(t, createEventIDFor(t, s, "PRJX-1"), got.UID,
		"UID must equal the node's create_node event id")
}

// TestCreateNode_UID_UniquePerNode — distinct nodes never share a UID.
func TestCreateNode_UID_UniquePerNode(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "a")))
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-2", "", "PRJX", "b")))
	a, _ := s.GetNode(ctx, "PRJX-1")
	b, _ := s.GetNode(ctx, "PRJX-2")
	assert.NotEqual(t, a.UID, b.UID)
}

// TestResolveByUID_RoundTrip — display_path <-> uid resolution.
func TestResolveByUID_RoundTrip(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	n, _ := s.GetNode(ctx, "PRJX-1")

	gotID, err := s.ResolveDisplayPathByUID(ctx, n.UID)
	require.NoError(t, err)
	assert.Equal(t, "PRJX-1", gotID)

	gotUID, err := s.ResolveUIDByDisplayPath(ctx, "PRJX-1")
	require.NoError(t, err)
	assert.Equal(t, n.UID, gotUID)
}

// TestResolveByUID_Unknown — resolving a UID that doesn't exist is a
// clean not-found, not a panic (edge case).
func TestResolveByUID_Unknown(t *testing.T) {
	s := newUIDTestStore(t)
	_, err := s.ResolveDisplayPathByUID(context.Background(), "0192-nonexistent")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestExport_CarriesUID — the UID travels with the node in exports so
// re-import stays consistent (ADR §7).
func TestExport_CarriesUID(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	n, _ := s.GetNode(ctx, "PRJX-1")

	data, err := s.Export(ctx, "PRJX", "test")
	require.NoError(t, err)
	require.Len(t, data.Nodes, 1)
	assert.Equal(t, n.UID, data.Nodes[0].UID, "export must carry the node UID")
}

// TestMigration_BackfillsUIDFromCreateEvent — Phase 0: an existing node
// missing a UID is backfilled from its create_node event id (deterministic
// and replica-consistent). Simulated by clearing the uid post-create and
// re-running the backfill.
func TestMigration_BackfillsUIDFromCreateEvent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	want := createEventIDFor(t, s, "PRJX-1")

	// Simulate a pre-migration row: clear the uid, then run the backfill.
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id = 'PRJX-1'`)
	require.NoError(t, err)
	require.NoError(t, s.BackfillUIDs(ctx))

	n, _ := s.GetNode(ctx, "PRJX-1")
	assert.Equal(t, want, n.UID, "backfill must restore uid = create event id")
}

// TestMigration_BackfillNoCreateEvent_LocalMint — edge case: a node with
// no recoverable create event gets a non-empty locally-minted uid (and is
// flagged). Safe because such data was never shared (ADR §7).
func TestMigration_BackfillNoCreateEvent_LocalMint(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	// Remove the create event AND clear the uid → no recoverable source.
	_, err = s.WriteDB().ExecContext(ctx,
		`DELETE FROM sync_events WHERE node_id = 'PRJX-1' AND op_type = 'create_node'`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id = 'PRJX-1'`)
	require.NoError(t, err)

	require.NoError(t, s.BackfillUIDs(ctx))
	n, _ := s.GetNode(ctx, "PRJX-1")
	assert.NotEmpty(t, n.UID, "no-create-event node must still get a (local) uid")
}

// TestBackfillUIDs_Idempotent — running the backfill twice changes
// nothing and never overwrites an already-set uid (corner case).
func TestBackfillUIDs_Idempotent(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	before, _ := s.GetNode(ctx, "PRJX-1")

	require.NoError(t, s.BackfillUIDs(ctx))
	require.NoError(t, s.BackfillUIDs(ctx))
	after, _ := s.GetNode(ctx, "PRJX-1")
	assert.Equal(t, before.UID, after.UID, "backfill must not change an existing uid")
}

// TestBackfillUIDs_Mixed — a store with one event-backed node and one
// orphan: the first gets its create-event id (deterministic), the second
// a non-empty local uid; both end up distinct and non-empty (edge case).
func TestBackfillUIDs_Mixed(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "has-event")))
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-2", "", "PRJX", "orphan")))
	want1 := createEventIDFor(t, s, "PRJX-1")
	// Make PRJX-2 an orphan (no create event) and clear both uids.
	_, err := s.WriteDB().ExecContext(ctx,
		`DELETE FROM sync_events WHERE node_id = 'PRJX-2' AND op_type = 'create_node'`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id IN ('PRJX-1','PRJX-2')`)
	require.NoError(t, err)

	require.NoError(t, s.BackfillUIDs(ctx))
	n1, _ := s.GetNode(ctx, "PRJX-1")
	n2, _ := s.GetNode(ctx, "PRJX-2")
	assert.Equal(t, want1, n1.UID, "event-backed node -> deterministic uid")
	assert.NotEmpty(t, n2.UID, "orphan -> local uid")
	assert.NotEqual(t, n1.UID, n2.UID)
}

// TestResolveUIDByDisplayPath_Deleted — a soft-deleted node does not
// resolve (edge case).
func TestResolveUIDByDisplayPath_Deleted(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "doomed")))
	require.NoError(t, s.DeleteNode(ctx, "PRJX-1", false, "test"))
	_, err := s.ResolveUIDByDisplayPath(ctx, "PRJX-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// newUIDTestStore is a local helper mirroring the package's store setup.
func newUIDTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var _ = sql.ErrNoRows // keep database/sql imported for future cases
