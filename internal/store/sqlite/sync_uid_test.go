// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// readEventUID returns the uid column for the sync_events row with the
// given event_id.
func readEventUID(t *testing.T, raw *sql.DB, eventID string) string {
	t.Helper()
	var uid sql.NullString
	require.NoError(t, raw.QueryRow(
		`SELECT uid FROM sync_events WHERE event_id = ?`, eventID).Scan(&uid))
	return uid.String
}

func readNodeUID(t *testing.T, raw *sql.DB, id string) string {
	t.Helper()
	var uid sql.NullString
	require.NoError(t, raw.QueryRow(
		`SELECT uid FROM nodes WHERE id = ?`, id).Scan(&uid))
	return uid.String
}

// TestEmit_CreateNode_SelfAnchorsUID is the REQUIRED create_node
// self-anchor case at EMIT time (ADR-003 §2): the create_node event's
// uid IS its own event_id, and that equals the node's stored uid.
func TestEmit_CreateNode_SelfAnchorsUID(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	node := newUIDTestNode("MTIX-1")
	require.NoError(t, s.CreateNode(ctx, node))

	// uid == event_id (self-anchor) on the create event row.
	require.Equal(t, node.UID, readEventUID(t, raw, node.UID),
		"create_node uid must equal its own event_id (ADR-003 §2 self-anchor)")
	require.Equal(t, node.UID, readNodeUID(t, raw, "MTIX-1"),
		"the node row's uid must equal the create-event id")
}

// TestEmit_NonCreate_CarriesNodeUID asserts every non-create op resolves
// the target node's uid from nodes.uid and carries it on the event
// (dual-carry: ADR-003 §3, §7 Phase 3).
func TestEmit_NonCreate_CarriesNodeUID(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	node := newUIDTestNode("MTIX-1")
	require.NoError(t, s.CreateNode(ctx, node))
	wantUID := node.UID

	// Emit a non-create (update_field) event for MTIX-1 directly through
	// the emitter so we test that emitEvent resolves nodes.uid for the
	// target and carries it on the event.
	payload, err := model.EncodePayload(&model.UpdateFieldPayload{
		FieldName: "title", NewValue: []byte(`"renamed"`),
	})
	require.NoError(t, err)
	var eventID string
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		p := emitParams{
			NodeID: "MTIX-1", ProjectCode: "MTIX",
			OpType: model.OpUpdateField, Author: "alice", Payload: payload,
		}
		if emitErr := emitEvent(ctx, tx, p); emitErr != nil {
			return emitErr
		}
		return tx.QueryRowContext(ctx,
			`SELECT event_id FROM sync_events WHERE op_type = 'update_field'
			 ORDER BY lamport_clock DESC LIMIT 1`).Scan(&eventID)
	}))
	require.Equal(t, wantUID, readEventUID(t, raw, eventID),
		"a non-create event must carry the target node's durable uid")
}

// TestApply_CreateNode_SelfAnchorsUID is the REQUIRED create_node
// self-anchor case at APPLY time (ADR-003 §2): applying a create_node
// persists nodes.uid = event_id.
func TestApply_CreateNode_SelfAnchorsUID(t *testing.T) {
	s, raw := applyTestStore(t)
	e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})
	e.UID = e.EventID // self-anchor, as the emitter sets it
	require.NoError(t, applyOnce(t, s, e))

	require.Equal(t, e.EventID, readNodeUID(t, raw, "MTIX-1"),
		"apply must persist nodes.uid = create-event id (ADR-003 §2)")
}

// TestApply_UIDKeyed_FollowsRenumber is the heart of ADR-003 §3/§10:
// a node's display_path can change (simulated renumber) while its uid is
// stable; a later event that carries the uid must act on the node by uid,
// NOT by the now-stale node_id.
func TestApply_UIDKeyed_FollowsRenumber(t *testing.T) {
	s, raw := applyTestStore(t)

	// create_node MTIX-1 (uid self-anchored).
	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "orig"})
	create.UID = create.EventID
	require.NoError(t, applyOnce(t, s, create))
	uid := create.EventID

	// Simulate a renumber: the display_path moves from MTIX-1 to MTIX-7,
	// uid unchanged. (30.6 does not own the renumber op; we move the row
	// directly to model the post-renumber state.)
	_, err := raw.Exec(`UPDATE nodes SET id = ? WHERE uid = ?`, "MTIX-7", uid)
	require.NoError(t, err)

	// A field update whose NodeID is the OLD path but whose UID is the
	// durable uid must still hit the (renumbered) node.
	upd := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: jsonString(t, "after-renumber")})
	upd.UID = uid
	require.NoError(t, applyOnce(t, s, upd))

	require.Equal(t, "after-renumber", readNodeColumn(t, raw, "MTIX-7", "title"),
		"a uid-keyed event must follow the node across a renumber (ADR-003 §3/§10)")
}

// TestApply_FallsBackToNodeID_WhenUIDAbsent is the dual-carry fallback
// (ADR-003 §7 Phase 3): an event with NO uid (an old CLI's event) keys
// apply on node_id exactly as before.
func TestApply_FallsBackToNodeID_WhenUIDAbsent(t *testing.T) {
	s, raw := applyTestStore(t)

	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "orig"})
	// No UID set — emulate an old-CLI create.
	require.NoError(t, applyOnce(t, s, create))

	upd := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: jsonString(t, "via-node-id")})
	// No UID — must fall back to node_id.
	require.NoError(t, applyOnce(t, s, upd))

	require.Equal(t, "via-node-id", readNodeColumn(t, raw, "MTIX-1", "title"),
		"a uid-less event must key apply on node_id (dual-carry fallback)")
}

// TestApply_CausalOrder_UIDMissingNodeSurfacesNotFound is HAZARD (c):
// an event for a node whose create_node hasn't applied yet must surface
// ErrNotFound (causal order), never silently mis-apply.
func TestApply_CausalOrder_UIDMissingNodeSurfacesNotFound(t *testing.T) {
	s, _ := applyTestStore(t)

	// An update carrying a uid for a node that was never created.
	upd := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2,
		&model.UpdateFieldPayload{FieldName: "title", NewValue: jsonString(t, "orphan")})
	upd.UID = "uid-never-created"
	err := applyOnce(t, s, upd)
	require.Error(t, err, "an event for an uncreated node must not silently apply")
	require.True(t, errors.Is(err, model.ErrNotFound),
		"must surface ErrNotFound for causal-order safety (ADR-003 hazard c)")
}

// TestApply_CreateNode_IdempotencyIsEventIDKeyed asserts that
// applyCreateNode's idempotency no longer rides "OR IGNORE" but the
// applied_events event_id check (FR-18.9 / ADR-003 §13): re-applying the
// SAME create event (same event_id/uid) is a no-op AND records nothing
// twice, with the node unchanged. This is the dedup path the old OR IGNORE
// used to double-cover; it must hold independently now.
func TestApply_CreateNode_IdempotencyIsEventIDKeyed(t *testing.T) {
	s, raw := applyTestStore(t)

	a := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "from-alice"})
	a.UID = a.EventID
	require.NoError(t, applyOnce(t, s, a))
	require.NoError(t, applyOnce(t, s, a), "re-applying the same create is a no-op")

	require.Equal(t, 1, countNodes(t, raw))
	require.Equal(t, 1, countApplied(t, raw), "applied_events records the event_id exactly once")
	require.Equal(t, "from-alice", readNodeColumn(t, raw, "MTIX-1", "title"))
}

// TestApply_CreateNode_DistinctCollisionFirstWinsNoWedge pins the residual
// MTIX-28 behavior 30.6 must PRESERVE (the flip is 30.7's job, ADR-003 §6):
// two DISTINCT create events (different uids) for the same display_path do
// NOT wedge the apply pipeline (no hard error) and the FIRST writer keeps
// the row — the second is dropped silently on the id-PK conflict. The
// existing e2e/sync_collision_test.go relies on exactly this.
func TestApply_CreateNode_DistinctCollisionFirstWinsNoWedge(t *testing.T) {
	s, raw := applyTestStore(t)

	a := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "from-alice"})
	a.UID = a.EventID
	require.NoError(t, applyOnce(t, s, a))

	b := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "bob", 2,
		&model.CreateNodePayload{Title: "from-bob"})
	b.UID = b.EventID // distinct uid, same display_path
	require.NoError(t, applyOnce(t, s, b),
		"a distinct-create collision must NOT hard-error the apply pipeline (30.7 layers the renumber)")

	// First writer wins; second is silently dropped (residual MTIX-28).
	require.Equal(t, "from-alice", readNodeColumn(t, raw, "MTIX-1", "title"))
	require.Equal(t, 1, countNodes(t, raw))
}

// TestApply_Delete_FollowsRenumberByUID covers the uid-aware delete path:
// a delete carrying a stable uid tombstones the node even after its
// display_path moved (ADR-003 §3), and a delete for a never-created node
// is a silent no-op (SYNC-DESIGN §8.3), not an error.
func TestApply_Delete_FollowsRenumberByUID(t *testing.T) {
	s, raw := applyTestStore(t)

	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})
	create.UID = create.EventID
	require.NoError(t, applyOnce(t, s, create))
	_, err := raw.Exec(`UPDATE nodes SET id = ? WHERE uid = ?`, "MTIX-42", create.EventID)
	require.NoError(t, err)

	del := makeApplyEvent(t, model.OpDelete, "MTIX-1", "alice", 2, nil)
	del.UID = create.EventID
	require.NoError(t, applyOnce(t, s, del))

	var deletedAt sql.NullString
	require.NoError(t, raw.QueryRow(
		`SELECT deleted_at FROM nodes WHERE uid = ?`, create.EventID).Scan(&deletedAt))
	require.True(t, deletedAt.Valid, "uid-keyed delete must tombstone the renumbered node")

	// Delete for a node that never existed: silent no-op.
	orphan := makeApplyEvent(t, model.OpDelete, "MTIX-9", "alice", 3, nil)
	orphan.UID = "uid-nonexistent"
	require.NoError(t, applyOnce(t, s, orphan),
		"delete on a non-existent node is a no-op, not an error (SYNC-DESIGN §8.3)")
}

// TestApply_LinkDep_FollowsRenumberByUID covers the uid-aware dependency
// edge path: a link_dep whose from-node was renumbered still attaches the
// edge to the current display path (ADR-003 §3) via fromIDForDepEdge.
func TestApply_LinkDep_FollowsRenumberByUID(t *testing.T) {
	s, raw := applyTestStore(t)

	from := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "from"})
	from.UID = from.EventID
	require.NoError(t, applyOnce(t, s, from))
	to := makeApplyEvent(t, model.OpCreateNode, "MTIX-2", "alice", 2,
		&model.CreateNodePayload{Title: "to"})
	to.UID = to.EventID
	require.NoError(t, applyOnce(t, s, to))

	// Renumber the from-node.
	_, err := raw.Exec(`UPDATE nodes SET id = ? WHERE uid = ?`, "MTIX-50", from.EventID)
	require.NoError(t, err)

	link := makeApplyEvent(t, model.OpLinkDep, "MTIX-1", "alice", 3,
		&model.LinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: string(model.DepTypeBlocks)})
	link.UID = from.EventID
	require.NoError(t, applyOnce(t, s, link))

	var n int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM dependencies WHERE from_id = ? AND to_id = ?`,
		"MTIX-50", "MTIX-2").Scan(&n))
	require.Equal(t, 1, n, "link_dep must attach to the renumbered from-node's current id")
}

// TestEmit_CreateNode_ForceBackfill_PreservesExistingUID is the MTIX-30.15
// emit-side fix (ADR-003 §2/§9): a create_node emitted for a node that
// ALREADY has a uid (nodes.uid non-empty) must carry that EXISTING uid, not
// a fresh self-anchor. This is what keeps a node's uid STABLE across a
// --force re-backfill: the re-emitted create_node names the same logical
// node, so the hub registry can treat it as a no-op instead of a false
// collision. A genuinely new node (no uid yet) still self-anchors.
func TestEmit_CreateNode_ForceBackfill_PreservesExistingUID(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	node := newUIDTestNode("MTIX-1")
	require.NoError(t, s.CreateNode(ctx, node))
	originalUID := node.UID
	require.NotEmpty(t, originalUID)

	// Wipe the event log (the v0.1.x-upgrader / re-backfill precondition);
	// nodes.uid survives because it lives on the nodes row.
	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)
	require.Equal(t, originalUID, readNodeUID(t, raw, "MTIX-1"),
		"nodes.uid must survive a sync_events wipe")

	// Re-emit a create_node for the SAME node WITHOUT supplying an EventID
	// (exactly what Backfill does). A fresh event_id is minted, but the
	// event's uid must be the node's EXISTING uid, not the fresh event_id.
	createPayload, err := buildCreateNodePayload(node)
	require.NoError(t, err)
	var freshEventID string
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		if emitErr := emitEvent(ctx, tx, emitParams{
			NodeID: "MTIX-1", ProjectCode: "MTIX",
			OpType: model.OpCreateNode, Author: "alice", Payload: createPayload,
		}); emitErr != nil {
			return emitErr
		}
		return tx.QueryRowContext(ctx,
			`SELECT event_id FROM sync_events WHERE op_type = 'create_node'
			 ORDER BY lamport_clock DESC LIMIT 1`).Scan(&freshEventID)
	}))

	require.NotEqual(t, originalUID, freshEventID,
		"the re-backfilled create_node must get a FRESH event_id")
	require.Equal(t, originalUID, readEventUID(t, raw, freshEventID),
		"a re-backfilled create_node for an existing node must carry the node's EXISTING uid (stable across --force backfill)")
}

// TestEmit_CreateNode_NewNode_StillSelfAnchors is the corner case that
// guards the fix: a genuinely NEW node — one whose nodes.uid is empty at
// emit time — must still self-anchor uid = its own event_id (ADR-003 §2).
// Only an EXISTING uid is preserved; a missing one is minted.
func TestEmit_CreateNode_NewNode_StillSelfAnchors(t *testing.T) {
	s, raw := emitTestStore(t)
	ctx := context.Background()

	// Insert a node row with an EMPTY uid (a pre-30.1 / not-yet-anchored
	// node), then emit its create_node directly through the emitter.
	node := newUIDTestNode("MTIX-2")
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return insertNode(ctx, tx, node)
	}))
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id = 'MTIX-2'`)
	require.NoError(t, err)

	payload, err := buildCreateNodePayload(node)
	require.NoError(t, err)
	var eventID string
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		if emitErr := emitEvent(ctx, tx, emitParams{
			NodeID: "MTIX-2", ProjectCode: "MTIX",
			OpType: model.OpCreateNode, Author: "alice", Payload: payload,
		}); emitErr != nil {
			return emitErr
		}
		return tx.QueryRowContext(ctx,
			`SELECT event_id FROM sync_events WHERE op_type = 'create_node'
			 ORDER BY lamport_clock DESC LIMIT 1`).Scan(&eventID)
	}))

	require.Equal(t, eventID, readEventUID(t, raw, eventID),
		"a genuinely new node (empty nodes.uid) must self-anchor uid = event_id")
}

// jsonString is a tiny helper for update_field NewValue payloads.
func jsonString(t *testing.T, s string) []byte {
	t.Helper()
	return []byte(`"` + s + `"`)
}

// newUIDTestNode builds a minimal valid root node for the uid tests.
func newUIDTestNode(id string) *model.Node {
	now := time.Now().UTC().Truncate(time.Second)
	return &model.Node{
		ID:          id,
		Project:     "MTIX",
		Title:       "root",
		Status:      model.StatusOpen,
		Priority:    model.PriorityMedium,
		Progress:    0.0,
		Weight:      1.0,
		NodeType:    model.NodeTypeStory,
		Creator:     "alice",
		ContentHash: model.ComputeContentHash("root", "", "", "", nil),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}
