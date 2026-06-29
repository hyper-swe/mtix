// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// TDD suite for RenumberForHubRejection (MTIX-30.7 / ADR-003 §6): the
// production drain a client runs when the hub rejects a create_node with
// renumber-required — re-claim the next free sibling number, renumber the
// node's subtree (uid stable), and re-queue the create event so the next
// push carries the distinct number. Mirrors the proven e2e resolveRenumber
// algorithm, now in the store so the CLI push loop and sync daemon share it.
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// createChild creates a child under parentID the way production does — by
// claiming the next sibling seq (advancing the counter) before insert — so
// ClaimNextSeq later returns a genuinely-next number. Returns the new id.
func createChild(t *testing.T, s interface {
	ClaimNextSeq(context.Context, string, string) (int, error)
	CreateNode(context.Context, *model.Node) error
}, parentID, project, title string) string {
	t.Helper()
	ctx := context.Background()
	seq, err := s.ClaimNextSeq(ctx, project, parentID)
	require.NoError(t, err)
	id := model.BuildID(project, parentID, seq)
	n := mkNode(id, parentID, project, title)
	n.Depth = 1
	n.Seq = seq
	n.NodeType = model.NodeTypeForDepth(1)
	n.ContentHash = n.ComputeHash()
	require.NoError(t, s.CreateNode(ctx, n))
	return id
}

// TestRenumberForHubRejection_RenumbersAndRequeues — the happy path: a
// hub-rejected create is moved to the next free number, its uid stays
// stable, and its create event is re-stamped to the new path + pending.
func TestRenumberForHubRejection_RenumbersAndRequeues(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "parent")))
	childID := createChild(t, s, "PRJX-1", "PRJX", "child") // PRJX-1.1
	c, _ := s.GetNode(ctx, childID)
	uid := c.UID
	require.NotEmpty(t, uid)

	newID, err := s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "PRJX-1.2", newID, "moves to the next free sibling number")

	// Old number is gone; the node lives at the new id with a STABLE uid.
	_, err = s.GetNode(ctx, childID)
	assert.ErrorIs(t, err, model.ErrNotFound)
	moved, err := s.GetNode(ctx, newID)
	require.NoError(t, err)
	assert.Equal(t, uid, moved.UID, "uid is stable across renumber (ADR-003 §2)")

	// The create event is re-stamped to the new path and re-queued.
	var nodeID, status string
	require.NoError(t, s.ReadDB().QueryRowContext(ctx,
		`SELECT node_id, sync_status FROM sync_events WHERE event_id = ? AND op_type = 'create_node'`,
		uid).Scan(&nodeID, &status))
	assert.Equal(t, newID, nodeID, "create event carries the new display path")
	assert.Equal(t, "pending", status, "re-queued for the next push")
}

// TestRenumberForHubRejection_LandsPastTakenSibling — EDGE: a sibling
// already occupies the next number, so the renumber lands past it (the
// monotonic counter never collides locally).
func TestRenumberForHubRejection_LandsPastTakenSibling(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "parent")))
	firstID := createChild(t, s, "PRJX-1", "PRJX", "a") // PRJX-1.1
	createChild(t, s, "PRJX-1", "PRJX", "b")            // PRJX-1.2
	c, _ := s.GetNode(ctx, firstID)

	newID, err := s.RenumberForHubRejection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, "PRJX-1.3", newID, "lands past the existing sibling, no collision")
	// The untouched sibling is intact.
	sib, err := s.GetNode(ctx, "PRJX-1.2")
	require.NoError(t, err)
	assert.Equal(t, "b", sib.Title)
}

// TestRenumberForHubRejection_UnknownUID — a uid with no live node is a
// clean error, not a panic.
func TestRenumberForHubRejection_UnknownUID(t *testing.T) {
	s := newUIDTestStore(t)
	_, err := s.RenumberForHubRejection(context.Background(), "0192-does-not-exist")
	assert.Error(t, err)
}
