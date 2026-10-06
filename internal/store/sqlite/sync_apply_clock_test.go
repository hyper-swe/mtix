// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Apply-time stamping is separate from authored event time and ordering.
func fixedReplayTime() time.Time {
	return time.Date(2001, 2, 3, 4, 5, 6, 123456789, time.FixedZone("test", 3600))
}

func applyWithStoreClock(t *testing.T, s *Store, e *model.SyncEvent) {
	t.Helper()
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return s.IdempotentApply(context.Background(), tx, e)
	}))
}

func replayClockCases() []struct {
	op      model.OpType
	payload any
} {
	until := fixedReplayTime().Add(time.Hour)
	return []struct {
		op      model.OpType
		payload any
	}{
		{model.OpUpdateField, &model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"updated"`)}},
		{model.OpClaim, &model.ClaimPayload{AgentID: "alice"}},
		{model.OpUnclaim, &model.UnclaimPayload{}},
		{model.OpDefer, &model.DeferPayload{Until: &until}},
		{model.OpTransitionStatus, &model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone}},
		{model.OpComment, &model.CommentPayload{AuthorID: "alice", Body: "comment"}},
		{model.OpLinkDep, &model.LinkDepPayload{DependsOnNodeID: "TEST-2", DepType: "blocks"}},
		{model.OpDelete, &model.DeletePayload{}},
		{model.OpSetAcceptance, &model.SetAcceptancePayload{AcceptanceText: "acceptance"}},
		{model.OpSetPrompt, &model.SetPromptPayload{PromptText: "prompt"}},
	}
}

func TestIdempotentApply_StoreClock_StampsEveryOperation(t *testing.T) {
	for _, tt := range replayClockCases() {
		t.Run(string(tt.op), func(t *testing.T) {
			s, raw := applyTestStore(t)
			s.SetClock(fixedReplayTime)
			for i, id := range []string{"TEST-1", "TEST-2"} {
				e := makeApplyEvent(t, model.OpCreateNode, id, "alice", int64(i+1), &model.CreateNodePayload{Title: "fixture"})
				applyWithStoreClock(t, s, e)
				requireReplayStamp(t, raw, e, fixedReplayTime())
			}
			e := makeApplyEvent(t, tt.op, "TEST-1", "alice", 3, tt.payload)
			e.WallClockTS = fixedReplayTime().Add(-time.Hour).UnixMilli()
			applyWithStoreClock(t, s, e)
			want := fixedReplayTime()
			if tt.op == model.OpComment {
				want = time.UnixMilli(e.WallClockTS)
			}
			requireReplayStamp(t, raw, e, want)
			if tt.op == model.OpTransitionStatus {
				require.Equal(t, time.UnixMilli(e.WallClockTS).UTC().Format(time.RFC3339), readNodeColumn(t, raw, e.NodeID, "closed_at"))
			}
		})
	}
}

func requireReplayStamp(t *testing.T, raw *sql.DB, e *model.SyncEvent, nodeAt time.Time) {
	t.Helper()
	var applied, mirrored, updated string
	require.NoError(t, raw.QueryRow(`SELECT applied_at FROM applied_events WHERE event_id = ?`, e.EventID).Scan(&applied))
	require.NoError(t, raw.QueryRow(`SELECT created_at FROM sync_events WHERE event_id = ?`, e.EventID).Scan(&mirrored))
	require.NoError(t, raw.QueryRow(`SELECT updated_at FROM nodes WHERE id = ?`, e.NodeID).Scan(&updated))
	require.Equal(t, fixedReplayTime().UTC().Format(time.RFC3339Nano), applied)
	require.Equal(t, applied, mirrored)
	require.Equal(t, nodeAt.UTC().Format(time.RFC3339), updated)
}

func TestIdempotentApply_StoreClock_EventTimeFallback(t *testing.T) {
	for _, op := range []model.OpType{model.OpComment, model.OpTransitionStatus} {
		t.Run(string(op), func(t *testing.T) {
			s, raw := applyTestStore(t)
			s.SetClock(fixedReplayTime)
			applyWithStoreClock(t, s, makeApplyEvent(t, model.OpCreateNode, "TEST-1", "alice", 1, &model.CreateNodePayload{Title: "fixture"}))
			var payload any = &model.CommentPayload{AuthorID: "alice", Body: "fallback"}
			if op == model.OpTransitionStatus {
				payload = &model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone}
			}
			e := makeApplyEvent(t, op, "TEST-1", "alice", 2, payload)
			e.WallClockTS = 253402300800000
			applyWithStoreClock(t, s, e)
			requireReplayStamp(t, raw, e, fixedReplayTime())
			n, err := s.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			if op == model.OpComment {
				require.Equal(t, fixedReplayTime().UTC(), n.Annotations[0].CreatedAt)
			} else {
				require.Equal(t, fixedReplayTime().UTC().Truncate(time.Second), *n.ClosedAt)
			}
		})
	}
}

func TestIdempotentApply_StoreClock_ConflictAndDedupe(t *testing.T) {
	s, raw := applyTestStore(t)
	s.SetClock(fixedReplayTime)
	create := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "alice", 1, &model.CreateNodePayload{Title: "fixture"})
	applyWithStoreClock(t, s, create)
	for _, lamp := range []int64{3, 2, 4} {
		e := makeApplyEvent(t, model.OpUpdateField, "TEST-1", "alice", lamp,
			&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"changed"`)})
		applyWithStoreClock(t, s, e)
	}
	var at string
	require.NoError(t, raw.QueryRow(`SELECT resolved_at FROM sync_conflicts LIMIT 1`).Scan(&at))
	require.Equal(t, fixedReplayTime().UTC().Format(time.RFC3339Nano), at)
	s.SetClock(func() time.Time { return fixedReplayTime().Add(time.Hour) })
	applyWithStoreClock(t, s, create)
	requireReplayStamp(t, raw, create, fixedReplayTime())
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM applied_events`).Scan(&n))
	require.Equal(t, 4, n)
}

func TestIdempotentApply_FreeCompatibilityAndNilClock_DefaultUTC(t *testing.T) {
	for _, useStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "free", true: "store reset"}[useStore], func(t *testing.T) {
			s, raw := applyTestStore(t)
			s.SetClock(fixedReplayTime)
			s.SetClock(nil)
			e := makeApplyEvent(t, model.OpCreateNode, "TEST-1", "alice", 1, &model.CreateNodePayload{Title: "default"})
			before := time.Now().UTC()
			require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
				if useStore {
					return s.IdempotentApply(context.Background(), tx, e)
				}
				return IdempotentApply(context.Background(), tx, e)
			}))
			var stored string
			require.NoError(t, raw.QueryRow(`SELECT applied_at FROM applied_events WHERE event_id = ?`, e.EventID).Scan(&stored))
			at, err := time.Parse(time.RFC3339Nano, stored)
			require.NoError(t, err)
			require.False(t, at.Before(before))
			require.False(t, at.After(time.Now().UTC()))
			require.Equal(t, time.UTC, at.Location())
		})
	}
}
