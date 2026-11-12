// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func rerunRetainedTask(t *testing.T) (*sqlite.Store, func() []*model.SyncEvent) {
	t.Helper()
	s, db := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	completed := time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC)
	s.SetClock(func() time.Time { return completed })
	for _, status := range []model.Status{model.StatusInProgress, model.StatusDone, model.StatusInvalidated, model.StatusOpen} {
		require.NoError(t, s.TransitionStatus(t.Context(), "MTIX-1", status, "fixture", "worker"))
	}
	return s, func() []*model.SyncEvent { return pushPendingOwnEvents(t, db) }
}
func TestWorkflowOps_AfterRerun_ReplicateStatusAndClosedAt(t *testing.T) {
	for _, name := range []string{"claim", "unclaim", "force reclaim", "unblock"} {
		t.Run(name, func(t *testing.T) {
			s, events := rerunRetainedTask(t)
			if name == "unblock" {
				mustCreateNode(t, s, "MTIX-2", "")
				require.NoError(t, s.AddDependency(t.Context(), &model.Dependency{FromID: "MTIX-2", ToID: "MTIX-1", DepType: model.DepTypeBlocks}))
				require.NoError(t, s.RemoveDependency(t.Context(), "MTIX-2", "MTIX-1", model.DepTypeBlocks))
			} else {
				require.NoError(t, s.ClaimNode(t.Context(), "MTIX-1", "worker"))
				if name == "unclaim" {
					require.NoError(t, s.UnclaimNode(t.Context(), "MTIX-1", "handoff", "worker"))
				}
				if name == "force reclaim" {
					s.SetClock(func() time.Time { return time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC) })
					require.NoError(t, s.ForceReclaimNode(t.Context(), "MTIX-1", "other", time.Hour))
				}
			}
			clone, _ := mutationTestStore(t)
			pullEvents(t, clone, events())
			want, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			got, err := clone.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.NotNil(t, want.ClosedAt)
			require.Equal(t, want.Status, got.Status)
			require.Equal(t, want.ClosedAt, got.ClosedAt)
		})
	}
}
func TestBackfill_AnnotatedOpenOrDoneNode_ReplicatesAnnotationsAndClosedAt(t *testing.T) {
	for _, status := range []model.Status{model.StatusOpen, model.StatusDone} {
		t.Run(string(status), func(t *testing.T) {
			s, db := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			at := time.Date(2026, 11, 1, 1, 2, 3, 123456789, time.UTC)
			note := model.Annotation{ID: "backfill-note", Author: "original", Text: "retained", CreatedAt: at, Addressee: "receiver", Resolved: true}
			raw, err := json.Marshal([]model.Annotation{note})
			require.NoError(t, err)
			_, err = db.Exec("UPDATE nodes SET status=?,closed_at=?,annotations=? WHERE id=?", status, at.Format(time.RFC3339Nano), string(raw), "MTIX-1")
			require.NoError(t, err)
			_, err = db.Exec("DELETE FROM sync_events")
			require.NoError(t, err)
			dry, err := s.BackfillDryRun(t.Context())
			require.NoError(t, err)
			result, err := s.Backfill(t.Context(), false)
			require.NoError(t, err)
			require.Equal(t, dry.TotalEvents, result.TotalEvents)
			clone, _ := mutationTestStore(t)
			pullEvents(t, clone, pushPendingOwnEvents(t, db))
			want, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			got, err := clone.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, want.Annotations, got.Annotations)
			require.Equal(t, want.ClosedAt, got.ClosedAt)
			require.Equal(t, want.Status, got.Status)
		})
	}
}
func TestAppendAnnotation_EmptyIdentity_RollsBack(t *testing.T) {
	s, db := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	before, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sync_events").Scan(&count))
	err = s.AppendAnnotation(t.Context(), "MTIX-1", model.Annotation{Text: "missing identity", Author: "author"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	after, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, before.Annotations, after.Annotations)
	var afterCount int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sync_events").Scan(&afterCount))
	require.Equal(t, count, afterCount)
}

func TestAnnotationReplication_AnyApplyOrder_PreservesUnrelatedAnnotations(t *testing.T) {
	source, db := mutationTestStore(t)
	mustCreateNode(t, source, "MTIX-1", "")
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	expected := []model.Annotation{{ID: "one", Text: "first", Author: "a", CreatedAt: at}, {ID: "two", Text: "middle", Author: "b", Addressee: "receiver", CreatedAt: at.Add(time.Second), Resolved: true}, {ID: "three", Text: "last", Author: "c", CreatedAt: at.Add(2 * time.Second)}}
	for _, n := range expected {
		n.Resolved = false
		require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", n))
	}
	require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", "two", true))
	original, err := source.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, expected, original.Annotations)
	events := pushPendingOwnEvents(t, db)
	for _, order := range [][]int{{1, 2, 3, 4}, {4, 3, 2, 1}, {2, 4, 1, 3}, {3, 1, 4, 2}} {
		clone, _ := mutationTestStore(t)
		pullEvents(t, clone, events[:1])
		for _, i := range order {
			pullEvents(t, clone, []*model.SyncEvent{events[i]})
		}
		pullEvents(t, clone, events)
		got, err := clone.GetNode(t.Context(), "MTIX-1")
		require.NoError(t, err)
		require.Equal(t, expected, got.Annotations)
	}
}

func TestRepairNodeStatus_LegacyDoneEvent_EmitsExactPayload(t *testing.T) {
	a, db := replicaWithPendingNode(t)
	a.SetClock(foreignWallClock)
	require.NoError(t, a.ClaimNode(t.Context(), "MTIX-1", "agent-a"))
	require.NoError(t, a.TransitionStatus(t.Context(), "MTIX-1", model.StatusDone, "finished", "agent-a"))
	done := eventIDOf(t, db, "MTIX-1", model.OpTransitionStatus)
	_, err := db.Exec("UPDATE sync_events SET wall_clock_ts=?,payload=json_remove(payload,'$.closed_at') WHERE event_id=?", foreignWallClock().UnixMilli(), done)
	require.NoError(t, err)
	clone, _ := mutationTestStore(t)
	pullEvents(t, clone, pushPendingOwnEvents(t, db))
	replayClaimAsBefore952(t, db, "MTIX-1", "agent-a")
	a.SetClock(func() time.Time { return foreignWallClock().Add(24 * time.Hour) })
	_, applied, err := a.RepairNodeStatus(t.Context(), "MTIX-1", "repairer", false)
	require.NoError(t, err)
	require.True(t, applied)
	var payload string
	require.NoError(t, db.QueryRow("SELECT payload FROM sync_events WHERE node_id=? AND author_id=? AND op_type=? ORDER BY lamport_clock DESC LIMIT 1", "MTIX-1", "repairer", model.OpTransitionStatus).Scan(&payload))
	require.Equal(t, `{"from":"in_progress","to":"done","reason":"sync repair"}`, payload)
	pullEvents(t, clone, pushPendingOwnEvents(t, db))
	got, err := clone.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, foreignClosedAt, got.ClosedAt.Format(time.RFC3339))
}

func TestBackfill_ZeroOrPreEpochAnnotationTime_PreservesCreatedAt(t *testing.T) {
	for _, at := range []time.Time{{}, time.Date(1960, 1, 2, 3, 4, 5, 123456789, time.UTC)} {
		t.Run(at.Format(time.RFC3339Nano), func(t *testing.T) {
			source, db := mutationTestStore(t)
			mustCreateNode(t, source, "MTIX-1", "")
			note := model.Annotation{ID: "old-date-note", Text: "retained", Author: "original", CreatedAt: at}
			require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", note))
			_, err := db.Exec("DELETE FROM sync_events")
			require.NoError(t, err)
			_, err = source.Backfill(t.Context(), false)
			require.NoError(t, err)
			clone, _ := mutationTestStore(t)
			pullEvents(t, clone, pushPendingOwnEvents(t, db))
			got, err := clone.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, []model.Annotation{note}, got.Annotations)
		})
	}
}

func TestBackfill_MissingAnnotationTime_UsesExactNodeCreatedAt(t *testing.T) {
	source, db := mutationTestStore(t)
	mustCreateNode(t, source, "MTIX-1", "")
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("offset", 19800))
	_, err := db.Exec("UPDATE nodes SET created_at=?, annotations=? WHERE id=?", at.Format(time.RFC3339Nano), `[{"text":"legacy note"}]`, "MTIX-1")
	require.NoError(t, err)
	_, err = db.Exec("DELETE FROM sync_events")
	require.NoError(t, err)
	_, err = source.Backfill(t.Context(), false)
	require.NoError(t, err)
	events := pushPendingOwnEvents(t, db)
	for _, e := range events {
		if e.OpType == model.OpComment {
			var payload model.CommentPayload
			require.NoError(t, json.Unmarshal(e.Payload, &payload))
			require.NotNil(t, payload.Annotation)
			require.Equal(t, at.Format(time.RFC3339Nano), payload.Annotation.CreatedAt.Format(time.RFC3339Nano))
		}
	}
	clone, _ := mutationTestStore(t)
	pullEvents(t, clone, events)
	got, err := clone.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	require.Equal(t, at.Format(time.RFC3339Nano), got.Annotations[0].CreatedAt.Format(time.RFC3339Nano))
}
