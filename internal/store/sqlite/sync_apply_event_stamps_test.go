// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// Replica determinism of stored times (MTIX-112.17.16.13; ADR-006 I1,
// FR-18.9): created_at and updated_at that a pull writes derive from the
// event's wall_clock_ts and never from the replica's own clock, so two
// replicas that apply the same events hold identical values whatever
// their clocks read.

const (
	stampCreatedWall = int64(1_788_264_000_500) // 2026-09-01T12:00:00.500Z
	stampOpWall      = int64(1_788_267_600_250) // 2026-09-01T13:00:00.250Z
	stampCreatedAt   = "2026-09-01T12:00:00Z"
	stampUpdatedAt   = "2026-09-01T13:00:00Z"
)

// stampOps lists every op that changes updated_at on a node row.
func stampOps(t *testing.T) []struct {
	name    string
	op      model.OpType
	payload any
} {
	t.Helper()
	return []struct {
		name    string
		op      model.OpType
		payload any
	}{
		{"update_field", model.OpUpdateField, titleUpdate(t, "renamed")},
		{"set_prompt", model.OpSetPrompt, &model.SetPromptPayload{PromptText: "p"}},
		{"set_acceptance", model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: "a"}},
		{"delete", model.OpDelete, &model.DeletePayload{}},
		{"comment", model.OpComment, &model.CommentPayload{AuthorID: "peer-b", Body: "n"}},
		{"transition_status", model.OpTransitionStatus, transition(model.StatusOpen, model.StatusInProgress)},
	}
}

// stampedReplica applies a create at stampCreatedWall and then op at
// stampOpWall on a fresh store whose clock reads applyClock.
func stampedReplica(t *testing.T, applyClock time.Time, op model.OpType, payload any) map[string]string {
	t.Helper()
	s, raw := mutationTestStore(t)
	s.SetClock(func() time.Time { return applyClock })
	create := pulledEvent(t, model.OpCreateNode, "MTIX-9",
		&model.CreateNodePayload{Title: "t", NodeType: model.NodeTypeStory, Creator: "peer-b"}, 1, stampCreatedWall)
	change := pulledEvent(t, op, "MTIX-9", payload, 2, stampOpWall)
	pullOnStoreClock(t, s, create, change)
	return nodeRow(t, raw, "MTIX-9")
}

// TestApply_PulledCreateNode_StampsEventTimeNotApplyClock: created_at and
// updated_at of a node a pull creates are the create event's time in whole
// seconds.
func TestApply_PulledCreateNode_StampsEventTimeNotApplyClock(t *testing.T) {
	t.Parallel()
	s, raw := mutationTestStore(t)
	s.SetClock(func() time.Time { return time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC) })
	create := pulledEvent(t, model.OpCreateNode, "MTIX-9",
		&model.CreateNodePayload{Title: "t", NodeType: model.NodeTypeStory, Creator: "peer-b"}, 1, stampCreatedWall)
	pullOnStoreClock(t, s, create)
	row := nodeRow(t, raw, "MTIX-9")
	require.Equal(t, stampCreatedAt, row["created_at"])
	require.Equal(t, stampCreatedAt, row["updated_at"])
}

// TestApply_PulledOps_StampUpdatedAtFromEventTime: each op that changes a node
// stamps updated_at with its event's time, not the apply clock, and created_at
// stays the create event's time.
func TestApply_PulledOps_StampUpdatedAtFromEventTime(t *testing.T) {
	t.Parallel()
	for _, tt := range stampOps(t) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			row := stampedReplica(t, time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC), tt.op, tt.payload)
			require.Equal(t, stampCreatedAt, row["created_at"])
			require.Equal(t, stampUpdatedAt, row["updated_at"])
		})
	}
}

// TestApply_PulledEvents_TwoReplicasDifferentClocksHoldSameStamps: two stores
// that apply the same events with apply clocks years apart agree on
// created_at and updated_at for every op.
func TestApply_PulledEvents_TwoReplicasDifferentClocksHoldSameStamps(t *testing.T) {
	t.Parallel()
	for _, tt := range stampOps(t) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a := stampedReplica(t, time.Date(2027, 3, 4, 5, 6, 7, 0, time.UTC), tt.op, tt.payload)
			b := stampedReplica(t, time.Date(2035, 9, 8, 7, 6, 5, 0, time.UTC), tt.op, tt.payload)
			require.Equal(t, a["created_at"], b["created_at"])
			require.Equal(t, a["updated_at"], b["updated_at"])
		})
	}
}

// pullOnStoreClock applies evs in one transaction through the store's own
// IdempotentApply, so the apply clock set with SetClock is the one used.
func pullOnStoreClock(t *testing.T, s *sqlite.Store, evs ...*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := s.IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	}))
}

// TestApply_PulledEvents_AnyArrivalOrder_HoldSameStamps: updated_at is the
// latest applied event's time whatever order the events arrive in, so two
// replicas that apply the same events in opposite orders hold identical rows
// (ADR-006 I1: the projection is a function of the event set, not arrival
// order).
func TestApply_PulledEvents_AnyArrivalOrder_HoldSameStamps(t *testing.T) {
	t.Parallel()
	mk := func(t *testing.T) []*model.SyncEvent {
		return []*model.SyncEvent{
			pulledEvent(t, model.OpUpdateField, "MTIX-9", titleUpdate(t, "early"), 2, stampOpWall),
			pulledEvent(t, model.OpSetPrompt, "MTIX-9", &model.SetPromptPayload{PromptText: "p"}, 3, stampOpWall+3_600_000),
			pulledEvent(t, model.OpComment, "MTIX-9", &model.CommentPayload{AuthorID: "peer-b", Body: "n"}, 4, stampOpWall+60_000),
		}
	}
	apply := func(t *testing.T, applyClock time.Time, order []int) map[string]string {
		s, raw := mutationTestStore(t)
		s.SetClock(func() time.Time { return applyClock })
		create := pulledEvent(t, model.OpCreateNode, "MTIX-9",
			&model.CreateNodePayload{Title: "t", NodeType: model.NodeTypeStory, Creator: "peer-b"}, 1, stampCreatedWall)
		evs := mk(t)
		ordered := []*model.SyncEvent{create}
		for _, i := range order {
			ordered = append(ordered, evs[i])
		}
		pullOnStoreClock(t, s, ordered...)
		return nodeRow(t, raw, "MTIX-9")
	}
	fwd := apply(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), []int{0, 2, 1})
	rev := apply(t, time.Date(2034, 1, 1, 0, 0, 0, 0, time.UTC), []int{1, 2, 0})
	require.Equal(t, "2026-09-01T14:00:00Z", fwd["updated_at"], "the latest event's time")
	require.Equal(t, fwd["updated_at"], rev["updated_at"])
	require.Equal(t, fwd["created_at"], rev["created_at"])
}

// TestApply_PulledOps_EarlierEventArrivingLate_KeepsLaterUpdatedAt: for every
// op that stamps updated_at, an event with an earlier time that arrives after
// a later one still applies its change but leaves updated_at at the later
// time (the MAX rule; ADR-006 I1: arrival order must not matter). The later
// event is a comment, or a prompt change when the op under test is the
// comment, so it never conflicts with the op under test.
func TestApply_PulledOps_EarlierEventArrivingLate_KeepsLaterUpdatedAt(t *testing.T) {
	t.Parallel()
	const lateWall = stampOpWall + 3_600_000 // 2026-09-01T14:00:00.250Z
	issueType, err := json.Marshal("bug")
	require.NoError(t, err)
	tests := []struct {
		name    string
		op      model.OpType
		payload any
		effect  map[string]string // columns that prove the earlier event applied
	}{
		{"claim", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, map[string]string{"status": "in_progress", "assignee": "agent-b"}},
		{"unclaim", model.OpUnclaim, &model.UnclaimPayload{}, map[string]string{"status": "open"}},
		{"defer", model.OpDefer, &model.DeferPayload{}, map[string]string{"status": "deferred"}},
		{"transition_status", model.OpTransitionStatus, transition(model.StatusOpen, model.StatusInProgress), map[string]string{"status": "in_progress"}},
		{"delete", model.OpDelete, &model.DeletePayload{}, map[string]string{"deleted_by": "peer-b"}},
		{"set_acceptance", model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: "a"}, map[string]string{"acceptance": "a"}},
		{"set_prompt", model.OpSetPrompt, &model.SetPromptPayload{PromptText: "p"}, map[string]string{"prompt": "p"}},
		{"update_field", model.OpUpdateField, titleUpdate(t, "renamed"), map[string]string{"title": "renamed"}},
		{"update_field issue_type", model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "issue_type", NewValue: issueType}, map[string]string{"issue_type": "bug"}},
		{"comment", model.OpComment, &model.CommentPayload{AuthorID: "peer-b", Body: "n"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, raw := mutationTestStore(t)
			s.SetClock(func() time.Time { return time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC) })
			create := pulledEvent(t, model.OpCreateNode, "MTIX-9",
				&model.CreateNodePayload{Title: "t", NodeType: model.NodeTypeStory, Creator: "peer-b"}, 1, stampCreatedWall)
			var later *model.SyncEvent
			if tt.op == model.OpComment {
				later = pulledEvent(t, model.OpSetPrompt, "MTIX-9", &model.SetPromptPayload{PromptText: "late"}, 2, lateWall)
			} else {
				later = pulledEvent(t, model.OpComment, "MTIX-9", &model.CommentPayload{AuthorID: "peer-b", Body: "late"}, 2, lateWall)
			}
			earlier := pulledEvent(t, tt.op, "MTIX-9", tt.payload, 3, stampOpWall)
			pullOnStoreClock(t, s, create, later, earlier)
			row := nodeRow(t, raw, "MTIX-9")
			require.Equal(t, "2026-09-01T14:00:00Z", row["updated_at"], "updated_at must stay at the later event's time")
			for col, want := range tt.effect {
				require.Equal(t, want, row[col], "the earlier event must still apply (%s)", col)
			}
		})
	}
}
