// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// The held workflow winner skips malformed events (MTIX-95.27). A workflow
// event whose payload the workflow payload rule rejects changes no column
// when it applies, so it must not count as its node's held winner either:
// otherwise an older valid event is applied or rejected depending on whether
// the malformed event arrived before it.

// heldRow is one workflow event mirrored into sync_events before the lookup.
type heldRow struct {
	id      string
	op      model.OpType
	payload string
	lamport int64
}

const (
	validClaimPayload = `{"agent_id":"agent-a"}`
	validDonePayload  = `{"from":"open","to":"done"}`
)

// heldRowEvent builds the foreign event for r, scoped to nodeID and uid.
func heldRowEvent(t *testing.T, r heldRow, nodeID, uid string) *model.SyncEvent {
	t.Helper()
	e := winnerTestEvent(t, r.id, r.op, nil, r.lamport)
	e.NodeID, e.UID = nodeID, uid
	e.Payload = json.RawMessage(r.payload)
	return e
}

// heldKeyAfter mirrors rows (as node nodeID, uid uid) and returns what
// latestHeldWorkflowKey reports for a later incoming claim on MTIX-1 carrying
// uid.
func heldKeyAfter(t *testing.T, rows []heldRow, nodeID, uid string) (workflowKey, bool) {
	t.Helper()
	s, _ := applyTestStore(t)
	ctx := context.Background()
	var (
		key   workflowKey
		found bool
	)
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			if err := mirrorIncomingEvent(ctx, tx, heldRowEvent(t, r, nodeID, uid)); err != nil {
				return err
			}
		}
		incoming := heldRowEvent(t, heldRow{"incoming", model.OpClaim, validClaimPayload, 1}, "MTIX-1", uid)
		var err error
		key, found, err = latestHeldWorkflowKey(ctx, tx, incoming)
		return err
	}))
	return key, found
}

// TestLatestHeldWorkflowKey_MalformedRows_SkippedInKeyOrder: the lookup walks
// the held workflow events from the highest key down and returns the first
// one whose payload the rule accepts; with none, nothing is held.
func TestLatestHeldWorkflowKey_MalformedRows_SkippedInKeyOrder(t *testing.T) {
	tests := []struct {
		name    string
		rows    []heldRow
		wantID  string // "" means no workflow event is held
		wantLam int64
	}{
		{"only malformed events held", []heldRow{
			{"bad-transition", model.OpTransitionStatus, `{"from":"open"}`, 3},
			{"bad-claim", model.OpClaim, `<<<not-json`, 4},
			{"bad-defer", model.OpDefer, `{"until":"next tuesday"}`, 5},
		}, "", 0},
		{"malformed above a valid claim", []heldRow{
			{"claim-2", model.OpClaim, validClaimPayload, 2},
			{"bad-transition", model.OpTransitionStatus, `{"from":"open","to":""}`, 3},
		}, "claim-2", 2},
		{"valid above malformed above valid", []heldRow{
			{"claim-2", model.OpClaim, validClaimPayload, 2},
			{"bad-defer", model.OpDefer, `<<<not-json`, 3},
			{"done-4", model.OpTransitionStatus, validDonePayload, 4},
		}, "done-4", 4},
		{"malformed wins the event_id tie", []heldRow{
			{"e-1", model.OpTransitionStatus, validDonePayload, 3},
			{"e-2", model.OpTransitionStatus, `null`, 3},
		}, "e-1", 3},
		{"unclaim is never malformed", []heldRow{
			{"unclaim-2", model.OpUnclaim, `<<<not-json`, 2},
			{"bad-claim", model.OpClaim, `{"agent_id":5}`, 3},
		}, "unclaim-2", 2},
	}
	for _, tt := range tests {
		for _, scope := range []struct{ name, nodeID, uid string }{
			{"by node_id", "MTIX-1", ""},
			{"by uid after a renumber", "MTIX-9", "uid-1"},
		} {
			t.Run(tt.name+" "+scope.name, func(t *testing.T) {
				key, found := heldKeyAfter(t, tt.rows, scope.nodeID, scope.uid)

				if tt.wantID == "" {
					require.False(t, found, "no well-formed workflow event is held, got %+v", key)
					return
				}
				require.True(t, found)
				require.Equal(t, workflowKey{lamport: tt.wantLam, eventID: tt.wantID}, key)
			})
		}
	}
}
