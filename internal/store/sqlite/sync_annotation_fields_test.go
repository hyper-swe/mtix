// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// Replication must preserve annotation data, including its stable identity and
// resolution. Resolving an annotation must not append a placeholder comment.
func TestSyncAnnotationFields_EmitApplyExport_PreservesOriginal(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		name := "ordinary"
		if resolved {
			name = "resolved"
		}
		t.Run(name, func(t *testing.T) {
			source, raw := mutationTestStore(t)
			mustCreateNode(t, source, "MTIX-1", "")
			annotation := model.Annotation{ID: "01ANOTHERANNOTATION00000000", Author: "sender", Text: "review this", Addressee: "receiver", CreatedAt: time.Date(2026, 11, 6, 1, 2, 3, 456000000, time.UTC)}
			require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", annotation))
			if resolved {
				require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", annotation.ID, true))
				require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", annotation.ID, true))
			}
			events := pushPendingOwnEvents(t, raw)
			clone, _ := mutationTestStore(t)
			pullEvents(t, clone, events)
			pullEvents(t, clone, events)
			want, err := source.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			got, err := clone.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			expected := annotation
			expected.Resolved = resolved
			require.Equal(t, []model.Annotation{expected}, want.Annotations)
			require.Equal(t, want.Annotations, got.Annotations)
			for _, s := range []*sqlite.Store{source, clone} {
				exported, err := s.Export(t.Context(), "MTIX", "test")
				require.NoError(t, err)
				require.Equal(t, want.Annotations, exported.Nodes[0].Annotations)
			}
		})
	}
}

func TestSyncAnnotationFields_UnresolveAndLateAppend_Converge(t *testing.T) {
	source, raw := mutationTestStore(t)
	mustCreateNode(t, source, "MTIX-1", "")
	annotation := model.Annotation{ID: "01ANNOTATIONFIRST0000000000", Author: "sender", Text: "first", CreatedAt: time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)}
	require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", annotation))
	require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", annotation.ID, true))
	require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", annotation.ID, false))
	events := pushPendingOwnEvents(t, raw)
	for _, order := range [][]int{{0, 1, 2, 3}, {0, 3, 2, 1}, {0, 2, 1, 3}, {0, 1, 3, 2}} {
		clone, _ := mutationTestStore(t)
		for _, i := range order {
			pullEvents(t, clone, []*model.SyncEvent{events[i]})
		}
		got, err := clone.GetNode(t.Context(), "MTIX-1")
		require.NoError(t, err)
		require.Equal(t, []model.Annotation{annotation}, got.Annotations)
	}
}

// Rerun transitions retain the author's actual closed_at, including a null
// value and the completion time retained through invalidation and reset.
func TestSyncRerunClosedAt_OriginAndCloneMatch(t *testing.T) {
	for _, target := range []model.Status{model.StatusInvalidated, model.StatusOpen} {
		t.Run(string(target), func(t *testing.T) {
			source, raw := mutationTestStore(t)
			mustCreateNode(t, source, "MTIX-1", "")
			completed := time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC)
			source.SetClock(func() time.Time { return completed })
			require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusInProgress, "start", "worker"))
			require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusDone, "done", "worker"))
			source.SetClock(func() time.Time { return completed.Add(time.Hour) })
			require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusInvalidated, "rerun", "worker"))
			if target == model.StatusOpen {
				require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", target, "reset by rerun", "worker"))
			}
			clone, _ := mutationTestStore(t)
			pullEvents(t, clone, pushPendingOwnEvents(t, raw))
			want, err := source.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			got, err := clone.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, want.Status, got.Status)
			require.Equal(t, want.ClosedAt, got.ClosedAt)
		})
	}
}

func TestSyncAnnotationFields_Resolution_KeepsResolverEventAuthor(t *testing.T) {
	t.Setenv("MTIX_AUTHOR_ID", "resolver")
	source, raw := mutationTestStore(t)
	mustCreateNode(t, source, "MTIX-1", "")
	annotation := model.Annotation{ID: "author-fixture", Author: "original-author", Text: "note", CreatedAt: time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)}
	require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", annotation))
	require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", annotation.ID, true))
	events := readEventsByOp(t, raw, model.OpComment)
	require.Len(t, events, 2)
	require.Equal(t, "resolver", events[1].AuthorID)
	var p model.CommentPayload
	require.NoError(t, json.Unmarshal([]byte(events[1].Payload), &p))
	require.Equal(t, "original-author", p.AuthorID)
	require.Empty(t, p.To, "a resolution sends no new addressed message")
}

// MTIX-112.17.16.20 tracks legacy identity migration. Old creation payloads
// omitted the source annotation ID, so a new resolution retains both rows.
// Pin this accepted residual without guessing identity from comment text.
func TestSyncAnnotationFields_LegacyIdentityResidual_MTIX112171620(t *testing.T) {
	source, raw := mutationTestStore(t)
	mustCreateNode(t, source, "MTIX-1", "")
	note := model.Annotation{ID: "original-legacy-ulid", Author: "sender", Text: "old note", Addressee: "worker", CreatedAt: time.Date(2026, 11, 6, 1, 2, 3, 123000000, time.UTC)}
	require.NoError(t, source.AppendAnnotation(t.Context(), "MTIX-1", note))
	require.NoError(t, source.ResolveAnnotation(t.Context(), "MTIX-1", note.ID, true))
	events := pushPendingOwnEvents(t, raw)
	var old map[string]any
	require.NoError(t, json.Unmarshal(events[1].Payload, &old))
	delete(old, "annotation") // Wire shape emitted by a pre-metadata writer.
	var err error
	events[1].Payload, err = json.Marshal(old)
	require.NoError(t, err)
	clone, _ := mutationTestStore(t)
	pullEvents(t, clone, events)
	pullEvents(t, clone, events)
	got, err := clone.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	legacy := note
	legacy.ID = events[1].EventID
	legacy.CreatedAt = time.UnixMilli(events[1].WallClockTS).UTC()
	resolved := note
	resolved.Resolved = true
	require.ElementsMatch(t, []model.Annotation{legacy, resolved}, got.Annotations, "accepted residual: unresolved legacy row and resolved source-ID row; nothing dropped")
}

func TestRepairNodeStatus_RerunSnapshot_KeepsCompletionTime(t *testing.T) {
	source, raw := mutationTestStore(t)
	completed := time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC)
	source.SetClock(func() time.Time { return completed })
	mustCreateNode(t, source, "MTIX-1", "")
	require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusInProgress, "start", "worker"))
	require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusDone, "done", "worker"))
	source.SetClock(func() time.Time { return completed.Add(time.Hour) })
	require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusInvalidated, "rerun", "worker"))
	require.NoError(t, source.TransitionStatus(t.Context(), "MTIX-1", model.StatusOpen, "reset by rerun", "worker"))
	clone, _ := mutationTestStore(t)
	pullEvents(t, clone, pushPendingOwnEvents(t, raw))
	_, err := raw.Exec(`UPDATE nodes SET status = 'done' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)
	_, applied, err := source.RepairNodeStatus(t.Context(), "MTIX-1", "repairer", true)
	require.NoError(t, err)
	require.True(t, applied)
	pullEvents(t, clone, pushPendingOwnEvents(t, raw))
	for _, s := range []*sqlite.Store{source, clone} {
		got, err := s.GetNode(t.Context(), "MTIX-1")
		require.NoError(t, err)
		require.Equal(t, model.StatusOpen, got.Status)
		require.Equal(t, &completed, got.ClosedAt, "repair must preserve the rerun winner's explicit timestamp")
	}
}
