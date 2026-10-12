// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAppendAnnotation_ConcurrentWriters_KeepsAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))

	const writers = 20
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.AppendAnnotation(ctx, "PROJ-1", model.Annotation{
				ID: fmt.Sprintf("A%02d", i), Author: "w", Text: fmt.Sprintf("t%d", i), CreatedAt: now,
			})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Len(t, got.Annotations, writers)
}

func TestResolveAnnotation_SetsAndClearsFlag_UnknownIDNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))
	for _, id := range []string{"A1", "A2"} {
		require.NoError(t, s.AppendAnnotation(ctx, "PROJ-1", model.Annotation{ID: id, Author: "w", Text: id, CreatedAt: now}))
	}

	require.NoError(t, s.ResolveAnnotation(ctx, "PROJ-1", "A2", true))
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.False(t, got.Annotations[0].Resolved)
	require.True(t, got.Annotations[1].Resolved)

	require.NoError(t, s.ResolveAnnotation(ctx, "PROJ-1", "A2", false))
	got, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.False(t, got.Annotations[1].Resolved)

	require.ErrorIs(t, s.ResolveAnnotation(ctx, "PROJ-1", "nope", true), model.ErrNotFound)
	got, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Len(t, got.Annotations, 2, "a failed resolve must not change the array")
}

func TestAppendAnnotation_CorruptStoredJSON_FailsWithoutOverwriting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))
	_, err := s.WriteDB().Exec(`UPDATE nodes SET annotations = ? WHERE id = ?`, `[{"id": "broken"`, "PROJ-1")
	require.NoError(t, err)

	require.Error(t, s.AppendAnnotation(ctx, "PROJ-1", model.Annotation{ID: "n"}))
	var raw string
	require.NoError(t, s.ReadDB().QueryRow(`SELECT annotations FROM nodes WHERE id = ?`, "PROJ-1").Scan(&raw))
	require.Equal(t, `[{"id": "broken"`, raw, "unreadable data must be left untouched, never overwritten")
}

func TestAppendAnnotation_MissingNode_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.ErrorIs(t, s.AppendAnnotation(ctx, "PROJ-404", model.Annotation{ID: "x"}), model.ErrNotFound)
	require.ErrorIs(t, s.ResolveAnnotation(ctx, "PROJ-404", "x", true), model.ErrNotFound)
}
