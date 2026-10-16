// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Tests of the pulled-id grammar check (MTIX-187): a pulled event whose node
// id, parent id or dependency endpoint is outside the node id grammar is
// quarantined with the reason "invalid node id" and never applied, through
// the same quarantine as every other refused event.

// invalidIDEvent returns a copy of the offline create of TEST-9 turned into
// a distinct event: its own event id and uid, the given op, node id and
// payload, stamped one Lamport tick after the create.
func invalidIDEvent(create *model.SyncEvent, op model.OpType, nodeID, payload string) *model.SyncEvent {
	e := *create
	e.EventID = create.EventID[:len(create.EventID)-4] + "beef"
	e.UID = e.EventID
	e.OpType = op
	e.NodeID = nodeID
	e.Payload = json.RawMessage(payload)
	e.LamportClock = create.LamportClock + 1
	return &e
}

// TestPull_InvalidNodeID_QuarantinedNotApplied: each id an event names is
// checked against the node id grammar before the event is applied. On a
// store without this check, the create of TEST-1é.4 applied and the other
// events were quarantined only for a missing row, if at all.
func TestPull_InvalidNodeID_QuarantinedNotApplied(t *testing.T) {
	tests := []struct {
		name    string
		op      model.OpType
		nodeID  string
		payload string
		badID   string
	}{
		{"node id with a non-ASCII segment", model.OpCreateNode, "TEST-1é.4",
			`{"title":"pulled","parent_id":"TEST-9","node_type":"issue"}`, "TEST-1é.4"},
		{"node id with a letter segment", model.OpCreateNode, "TEST-9.x",
			`{"title":"pulled","parent_id":"TEST-9","node_type":"issue"}`, "TEST-9.x"},
		{"node id with an empty segment", model.OpCreateNode, "TEST-9..1",
			`{"title":"pulled","parent_id":"TEST-9","node_type":"issue"}`, "TEST-9..1"},
		{"node id with a lowercase prefix", model.OpCreateNode, "test-10",
			`{"title":"pulled","node_type":"issue"}`, "test-10"},
		{"parent id with a non-ASCII segment", model.OpCreateNode, "TEST-9.1",
			`{"title":"pulled","parent_id":"TEST-9é","node_type":"issue"}`, "TEST-9é"},
		{"update of a node with an invalid id", model.OpUpdateField, "TEST-9é",
			`{"field_name":"description","new_value":"x"}`, "TEST-9é"},
		{"link_dep endpoint", model.OpLinkDep, "TEST-9",
			`{"depends_on_node_id":"TEST-1é.4","dep_type":"blocks"}`, "TEST-1é.4"},
		{"unlink_dep endpoint", model.OpUnlinkDep, "TEST-9",
			`{"depends_on_node_id":"TEST-1é.4","dep_type":"blocks"}`, "TEST-1é.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			create := offlineEvents(t)[0]
			bad := invalidIDEvent(create, tt.op, tt.nodeID, tt.payload)

			held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull,
				[]*model.SyncEvent{create, bad})

			require.NoError(t, err, "a quarantined event does not fail the pull")
			require.Contains(t, held, bad.EventID)
			requireNotApplied(t, bad)
			q := requireQuarantinedAs(t, bad, "pull", "invalid node id")
			require.Contains(t, q.Reason, tt.badID, "the reason names the id")
			require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM nodes`),
				"only the valid create applied")
			require.Equal(t, create.LamportClock, peerLamport(t),
				"a quarantined event never advances the local clock")
		})
	}
}

// TestPull_ValidNodeIDs_Applied: ids inside the grammar still apply: a
// prefix with an underscore (the sync prefix grammar admits it,
// MTIX-95.17) and a provisional uid segment.
func TestPull_ValidNodeIDs_Applied(t *testing.T) {
	tests := []struct {
		name   string
		nodeID string
		parent string
	}{
		{"numeric child", "TEST-9.1", "TEST-9"},
		{"provisional uid segment", "TEST-9.u0193fa00", "TEST-9"},
		{"prefix with an underscore", "DEP_ADD-1", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			create := offlineEvents(t)[0]
			payload := `{"title":"pulled","node_type":"issue","parent_id":"` + tt.parent + `"}`
			good := invalidIDEvent(create, model.OpCreateNode, tt.nodeID, payload)

			held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull,
				[]*model.SyncEvent{create, good})

			require.NoError(t, err)
			require.Empty(t, held)
			require.Empty(t, quarantined(t))
			_, err = app.store.GetNode(ctx, tt.nodeID)
			require.NoError(t, err)
		})
	}
}
