// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// outOfOrderAnnotations returns annotations newest-first with equal timestamps
// whose ids are out of order.
func outOfOrderAnnotations() []model.Annotation {
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	return []model.Annotation{
		{ID: "D", Text: "d", CreatedAt: at.Add(time.Hour)},
		{ID: "C", Text: "c", CreatedAt: at},
		{ID: "B", Text: "b", CreatedAt: at},
		{ID: "A", Text: "a", CreatedAt: at.Add(-time.Hour)},
	}
}

func TestSetAnnotations_OutOfOrder_StoredInCanonicalOrder(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	in := outOfOrderAnnotations()
	require.NoError(t, s.SetAnnotations(context.Background(), "MTIX-1", in))

	want := annotationIDs(model.SortAnnotations(in))
	var stored []model.Annotation
	require.NoError(t, json.Unmarshal([]byte(readNodeColumn(t, raw, "MTIX-1", "annotations")), &stored))
	require.Equal(t, want, annotationIDs(stored), "stored order is (created_at, id)")
}

func TestAppendAnnotation_OlderThanStored_StoredInCanonicalOrder(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "B", CreatedAt: at}))
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "A", CreatedAt: at}))

	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B"}, annotationIDs(n.Annotations))
}

func TestApply_Comment_OrderMatchesSortAnnotations(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	require.NoError(t, s.SetAnnotations(ctx, "MTIX-1", outOfOrderAnnotations()))
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpComment, "MTIX-1", "alice", 2,
		&model.CommentPayload{AuthorID: "alice", Body: "synced"})))

	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, annotationIDs(model.SortAnnotations(n.Annotations)), annotationIDs(n.Annotations),
		"apply stores the order SortAnnotations defines")
}

// TestGetNode_AnnotationsStoredInInsertionOrder_ListedCanonically verifies a
// row saved before MTIX-100.2, with annotations in insertion order, lists in
// (created_at, id) order when the node is read.
func TestGetNode_AnnotationsStoredInInsertionOrder_ListedCanonically(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	stored, err := json.Marshal(outOfOrderAnnotations())
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET annotations = ? WHERE id = ?`, string(stored), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []string{"D", "C", "B", "A"},
		annotationIDs(func() []model.Annotation {
			var out []model.Annotation
			require.NoError(t, json.Unmarshal([]byte(readNodeColumn(t, raw, "MTIX-1", "annotations")), &out))
			return out
		}()), "the row holds the old insertion order")

	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C", "D"}, annotationIDs(n.Annotations))
}
