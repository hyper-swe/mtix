// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func permissionOnceAnnotation() model.Annotation {
	return model.Annotation{ID: "0123456789abcdef0123456789abcdef", Author: "mtix-permissions",
		Text:      "wake permissions changed on a host (see that host's mtix doctor)",
		CreatedAt: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
}

func TestAppendAnnotationOnce_Replay_NoExtraEventAndConflictingIDRefused(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	annotation := permissionOnceAnnotation()
	require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", annotation))
	require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", annotation))
	events := readEventsByOp(t, raw, model.OpComment)
	require.Len(t, events, 1)
	var id string
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT event_id FROM sync_events WHERE op_type = ?`, model.OpComment).Scan(&id))
	require.Equal(t, annotation.ID, id, "the synced event retains the stable recovery identity")
	var payload model.CommentPayload
	require.NoError(t, json.Unmarshal([]byte(events[0].Payload), &payload))
	require.Equal(t, annotation.Text, payload.Body)
	require.Equal(t, annotation.Author, payload.AuthorID)
	require.Empty(t, payload.To)
	annotation.Text = "conflicting value"
	require.ErrorIs(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", annotation), model.ErrConflict)
	require.Len(t, readEventsByOp(t, raw, model.OpComment), 1)
	got, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []model.Annotation{permissionOnceAnnotation()}, got.Annotations)
}

func TestAppendAnnotationOnce_ConcurrentRecovery_OneAnnotationAndEvent(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	errors := make([]error, 16)
	var joined sync.WaitGroup
	for i := range errors {
		joined.Add(1)
		go func(index int) {
			defer joined.Done()
			errors[index] = s.AppendAnnotationOnce(ctx, "MTIX-1", permissionOnceAnnotation())
		}(i)
	}
	joined.Wait()
	for _, err := range errors {
		require.NoError(t, err)
	}
	require.NoError(t, ctx.Err())
	require.Len(t, readEventsByOp(t, raw, model.OpComment), 1)
	got, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []model.Annotation{permissionOnceAnnotation()}, got.Annotations)
}

func TestAppendAnnotationOnce_IndependentSameBodyBatches_BothPreserved(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	first, second := permissionOnceAnnotation(), permissionOnceAnnotation()
	second.ID = "fedcba9876543210fedcba9876543210"
	require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", first))
	require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", second))
	require.Len(t, readEventsByOp(t, raw, model.OpComment), 2)
	got, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []model.Annotation{first, second}, got.Annotations)
}
