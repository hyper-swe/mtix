// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of the re-addressing of pending events after a hub-rejection
// renumber (MTIX-95.37). INVARIANT: no pending event of a renumbered task
// still names the old number, whichever kind of event it is.

// pendingNames returns node_id and payload of every pending event, by event id.
func pendingNames(t *testing.T, s *sqlite.Store) map[string][2]string {
	t.Helper()
	rows, err := s.ReadDB().QueryContext(context.Background(),
		`SELECT event_id, node_id, payload FROM sync_events WHERE sync_status = 'pending'`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string][2]string{}
	for rows.Next() {
		var id, node, payload string
		require.NoError(t, rows.Scan(&id, &node, &payload))
		out[id] = [2]string{node, payload}
	}
	require.NoError(t, rows.Err())
	return out
}

// subtreeFixture builds PRJX-1 with child PRJX-1.1 and grandchild PRJX-1.1.1,
// a task PRJX-2 outside it, and one pending event of every kind about them.
func subtreeFixture(t *testing.T) (s *sqlite.Store, uid string) {
	t.Helper()
	ctx := context.Background()
	s = newUIDTestStore(t)
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-2", "", "PRJX", "other")))
	child := createChild(t, s, "PRJX-1", "PRJX", "child")
	grand := createChild(t, s, child, "PRJX", "grandchild")
	require.Equal(t, "PRJX-1.1.1", grand)
	title := "root retitled"
	require.NoError(t, s.UpdateNode(ctx, "PRJX-1", &store.NodeUpdate{Title: &title}))
	require.NoError(t, s.ClaimNode(ctx, "PRJX-1.1", "agent-a"))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "PRJX-2", ToID: "PRJX-1.1", DepType: model.DepTypeBlocks}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "PRJX-1", ToID: "PRJX-2", DepType: model.DepTypeRelated}))
	require.NoError(t, s.RemoveDependency(ctx, "PRJX-1", "PRJX-2", model.DepTypeRelated))
	n, err := s.GetNode(ctx, "PRJX-1")
	require.NoError(t, err)
	return s, n.UID
}

// TestRenumberForHubRejection_ReaddressesEveryPendingEventOfTheSubtree: after
// the renumber, no pending event names the old number, in its node_id or in
// the parent or link target of its payload; the events of PRJX-2 are
// untouched, and each moved event names the number of the node it is about.
func TestRenumberForHubRejection_ReaddressesEveryPendingEventOfTheSubtree(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	before := pendingNames(t, s)

	newID, err := s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, "PRJX-3", newID)

	after := pendingNames(t, s)
	require.Len(t, after, len(before), "no event is added or lost")
	for id, names := range after {
		assert.NotRegexp(t, `^PRJX-1(\.|$)`, names[0], "event %s still names the old number", id)
		assert.NotRegexp(t, `"PRJX-1(\.[0-9.]+)?"`, names[1], "payload of %s still names the old number", id)
	}
	var movedLinks int
	for _, names := range after {
		var p struct {
			To       string `json:"depends_on_node_id"`
			ParentID string `json:"parent_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(names[1]), &p))
		if p.To == "PRJX-3.1" {
			movedLinks++
		}
	}
	assert.Equal(t, 1, movedLinks, "the link into the subtree points at the child's new number")
	for id, names := range before {
		if names[0] == "PRJX-2" && after[id][0] != "PRJX-2" {
			t.Errorf("event %s of the task outside the subtree moved to %s", id, after[id][0])
		}
	}
	for _, id := range []string{"PRJX-3", "PRJX-3.1", "PRJX-3.1.1"} {
		var n int
		require.NoError(t, s.ReadDB().QueryRowContext(ctx, `
			SELECT COUNT(*) FROM sync_events e JOIN nodes n ON n.uid = e.uid AND n.uid <> ''
			WHERE n.id = ? AND e.node_id <> n.id`, id).Scan(&n))
		assert.Zero(t, n, "every event about %s names %s", id, id)
	}
}

// TestRenumberForHubRejection_LeavesPushedEventsAsTheyAre: an event the hub
// already holds is history; the renumber does not rewrite it.
func TestRenumberForHubRejection_LeavesPushedEventsAsTheyAre(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE sync_events SET sync_status = 'pushed' WHERE op_type = 'update_field'`)
	require.NoError(t, err)

	_, err = s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)

	var node string
	require.NoError(t, s.ReadDB().QueryRowContext(ctx,
		`SELECT node_id FROM sync_events WHERE op_type = 'update_field' AND sync_status = 'pushed'`).Scan(&node))
	assert.Equal(t, "PRJX-1", node, "a pushed event keeps the number it was sent under")
}

// TestRenumberForHubRejection_RenumbersTwice_FollowsTheIntermediateNumber: a
// creation the hub rejects again after a failed push is renumbered a second
// time, and the events follow it, since they are found by uid.
func TestRenumberForHubRejection_RenumbersTwice_FollowsTheIntermediateNumber(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	first, err := s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)
	second, err := s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)
	require.NotEqual(t, first, second)

	for id, names := range pendingNames(t, s) {
		assert.NotRegexp(t, `^PRJX-(1|`+first[len("PRJX-"):]+`)(\.|$)`, names[0], "event %s", id)
		assert.NotContains(t, names[1], `"`+first, "payload of %s", id)
	}
}

// TestRenumberForHubRejection_DeletedTask_RenumberedWithItsDelete: a task
// deleted locally is rejected by the hub like any other; before MTIX-95.37
// the renumber failed with not found on every push. The task moves, soft
// deleted, and its delete event follows.
func TestRenumberForHubRejection_DeletedTask_RenumberedWithItsDelete(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "root")))
	n, err := s.GetNode(ctx, "PRJX-1")
	require.NoError(t, err)
	require.NoError(t, s.DeleteNode(ctx, "PRJX-1", false, "agent-a"))

	newID, err := s.RenumberForHubRejection(ctx, n.UID)
	require.NoError(t, err)
	require.NotEqual(t, "PRJX-1", newID)

	var deleted sql.NullString
	require.NoError(t, s.ReadDB().QueryRowContext(ctx,
		`SELECT deleted_at FROM nodes WHERE id = ?`, newID).Scan(&deleted))
	assert.True(t, deleted.Valid, "the moved task is still deleted")
	for id, names := range pendingNames(t, s) {
		assert.Equal(t, newID, names[0], "event %s follows the task", id)
	}
}

// TestRenumberForHubRejection_UnknownUID_ReturnsNotFound: the renumber of a
// uid no node holds is an error, not a no-op.
func TestRenumberForHubRejection_UnknownUID_ReturnsNotFound(t *testing.T) {
	s := newUIDTestStore(t)
	for _, uid := range []string{"", "uid-never-created"} {
		_, err := s.RenumberForHubRejection(context.Background(), uid)
		require.ErrorIs(t, err, model.ErrNotFound, "uid %q", uid)
	}
}

// TestRenumberForHubRejection_EventsWithoutUID_ReaddressedByNumber: a pending
// event queued before events carried a uid names its task by number alone, so
// the renumber swaps the old prefix for the new one.
func TestRenumberForHubRejection_EventsWithoutUID_ReaddressedByNumber(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET uid = NULL WHERE op_type <> 'create_node'`)
	require.NoError(t, err)

	_, err = s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)

	var claimNode, updateNode string
	require.NoError(t, s.ReadDB().QueryRowContext(ctx,
		`SELECT node_id FROM sync_events WHERE op_type = 'claim'`).Scan(&claimNode))
	require.NoError(t, s.ReadDB().QueryRowContext(ctx,
		`SELECT node_id FROM sync_events WHERE op_type = 'update_field'`).Scan(&updateNode))
	assert.Equal(t, "PRJX-3.1", claimNode, "the child's claim follows its subtree")
	assert.Equal(t, "PRJX-3", updateNode, "the root's edit follows it")
	for id, names := range pendingNames(t, s) {
		assert.NotRegexp(t, `^PRJX-1(\.|$)`, names[0], "event %s", id)
	}
}
