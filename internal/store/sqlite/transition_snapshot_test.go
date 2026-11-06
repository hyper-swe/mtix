// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// Optional timestamps retain legacy interpretation; explicit null and exact
// values represent the source row, including reset/invalidation residuals.
func TestTransitionSnapshot_OptionalTimestamp_DecodesStrictly(t *testing.T) {
	cases := []struct {
		name, raw    string
		absent, fail bool
		want         any
	}{
		{"legacy", "", true, false, nil},
		{"clear", "null", false, false, nil},
		{"value", `"2026-11-06T08:00:00+05:30"`, false, false, "2026-11-06T08:00:00+05:30"},
		{"fractional", `"2026-11-06T02:30:00.123456789Z"`, false, false, "2026-11-06T02:30:00.123456789Z"},
		{"bad time", `"yesterday"`, false, true, nil},
		{"wrong type", `42`, false, true, nil},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeClosedAtSnapshot(json.RawMessage(tt.raw))
			if tt.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.absent {
				require.Nil(t, got)
				return
			}
			require.Equal(t, &workflowColumn{write: true, value: tt.want}, got)
		})
	}
}

func TestAnnotationSnapshot_LegacyCreationAfterResolution_NoDuplicate(t *testing.T) {
	s, _ := applyTestStore(t)
	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "sender", 1, &model.CreateNodePayload{Title: "task"})
	old := makeApplyEvent(t, model.OpComment, "MTIX-1", "sender", 2, &model.CommentPayload{AuthorID: "sender", Body: "old", To: "receiver"})
	resolution := makeApplyEvent(t, model.OpComment, "MTIX-1", "sender", 3, &model.CommentPayload{AuthorID: "sender", Body: "old", Annotation: &model.CommentAnnotationState{ID: old.EventID, CreatedAt: eventTime(old.WallClockTS, s.clock()), Resolved: true, Addressee: "receiver"}})
	require.NoError(t, applyOnce(t, s, create))
	require.NoError(t, applyOnce(t, s, resolution))
	require.NoError(t, applyOnce(t, s, old))
	got, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)
	require.True(t, got.Annotations[0].Resolved)
	require.Equal(t, "receiver", got.Annotations[0].Addressee)
}

func TestTransitionSnapshot_Apply_ExplicitStateAndLegacyFallback(t *testing.T) {
	cases := []struct{ name, extra, want string }{
		{"legacy", "", ""},
		{"clear", `,"closed_at":null`, ""},
		{"retained", `,"closed_at":"2025-01-02T03:04:05Z"`, "2025-01-02T03:04:05Z"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "sender", 1, &model.CreateNodePayload{Title: "task"})))
			e := makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "sender", 2, &model.TransitionStatusPayload{From: model.StatusInvalidated, To: model.StatusOpen})
			e.Payload = json.RawMessage(`{"from":"invalidated","to":"open"` + tt.extra + `}`)
			require.NoError(t, applyOnce(t, s, e))
			require.NoError(t, applyOnce(t, s, e))
			require.Equal(t, tt.want, readNodeColumn(t, raw, "MTIX-1", "closed_at"))
		})
	}
}

func TestAnnotationSnapshot_ConcurrentTie_StableWinner(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprint(reverse), func(t *testing.T) {
			s, _ := applyTestStore(t)
			require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "sender", 1, &model.CreateNodePayload{Title: "task"})))
			first := makeApplyEvent(t, model.OpComment, "MTIX-1", "one", 2, &model.CommentPayload{})
			last := makeApplyEvent(t, model.OpComment, "MTIX-1", "two", 2, &model.CommentPayload{})
			if first.EventID > last.EventID {
				first, last = last, first
			}
			at := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
			first.Payload = mustSnapshotPayload(t, &model.CommentPayload{Body: "note", AuthorID: "author", Annotation: &model.CommentAnnotationState{ID: "stable", CreatedAt: at, Resolved: true}})
			last.Payload = mustSnapshotPayload(t, &model.CommentPayload{Body: "note", AuthorID: "author", Annotation: &model.CommentAnnotationState{ID: "stable", CreatedAt: at, Resolved: false}})
			events := []*model.SyncEvent{first, last}
			if reverse {
				events = []*model.SyncEvent{last, first}
			}
			for _, e := range events {
				require.NoError(t, applyOnce(t, s, e))
			}
			got, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Len(t, got.Annotations, 1)
			require.False(t, got.Annotations[0].Resolved)
		})
	}
}

func mustSnapshotPayload(t *testing.T, p *model.CommentPayload) json.RawMessage {
	t.Helper()
	b, err := model.EncodePayload(p)
	require.NoError(t, err)
	return b
}
