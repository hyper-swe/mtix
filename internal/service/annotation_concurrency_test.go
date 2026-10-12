// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

const annotationRaceWriters = 24

// runConcurrently starts n goroutines behind one start gate and joins them all.
func runConcurrently(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// TestAddAnnotation_ConcurrentWriters_PersistsEveryAnnotation verifies that N
// parallel comments on one node keep exactly N annotations: no lost update
// from a read-modify-write outside the write transaction (MTIX-148, FR-3.4).
func TestAddAnnotation_ConcurrentWriters_PersistsEveryAnnotation(t *testing.T) {
	for round := 0; round < 5; round++ {
		promptSvc, nodeSvc, _, _ := newTestPromptService(t)
		ctx := context.Background()
		node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "PROJ", Title: "Race", Creator: "admin",
		})
		require.NoError(t, err)

		errs := runConcurrently(annotationRaceWriters, func(i int) error {
			return promptSvc.AddAnnotation(ctx, node.ID, fmt.Sprintf("comment-%d", i), "agent", "")
		})
		for _, e := range errs {
			require.NoError(t, e)
		}

		got, err := nodeSvc.GetNode(ctx, node.ID)
		require.NoError(t, err)
		require.Len(t, got.Annotations, annotationRaceWriters, "round %d lost annotations", round)
		texts := make(map[string]bool)
		for _, a := range got.Annotations {
			texts[a.Text] = true
		}
		require.Len(t, texts, annotationRaceWriters, "every distinct comment must be stored")
	}
}

// TestResolveAnnotation_ConcurrentResolvers_ResolvesEveryAnnotation verifies
// that parallel resolves of different annotations all stick (MTIX-148).
func TestResolveAnnotation_ConcurrentResolvers_ResolvesEveryAnnotation(t *testing.T) {
	for round := 0; round < 5; round++ {
		promptSvc, nodeSvc, _, _ := newTestPromptService(t)
		ctx := context.Background()
		node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "PROJ", Title: "Resolve race", Creator: "admin",
		})
		require.NoError(t, err)
		for i := 0; i < annotationRaceWriters; i++ {
			require.NoError(t, promptSvc.AddAnnotation(ctx, node.ID, fmt.Sprintf("c-%d", i), "agent", ""))
		}
		seeded, err := nodeSvc.GetNode(ctx, node.ID)
		require.NoError(t, err)
		require.Len(t, seeded.Annotations, annotationRaceWriters)

		errs := runConcurrently(annotationRaceWriters, func(i int) error {
			return promptSvc.ResolveAnnotation(ctx, node.ID, seeded.Annotations[i].ID, true)
		})
		for _, e := range errs {
			require.NoError(t, e)
		}

		got, err := nodeSvc.GetNode(ctx, node.ID)
		require.NoError(t, err)
		require.Len(t, got.Annotations, annotationRaceWriters)
		for _, a := range got.Annotations {
			require.True(t, a.Resolved, "annotation %s lost its resolved flag in round %d", a.ID, round)
		}
	}
}

// TestAnnotations_ConcurrentAppendAndResolve_KeepsBoth verifies that appends
// racing with resolves neither drop the new annotations nor the resolved flags.
func TestAnnotations_ConcurrentAppendAndResolve_KeepsBoth(t *testing.T) {
	promptSvc, nodeSvc, _, _ := newTestPromptService(t)
	ctx := context.Background()
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Mixed race", Creator: "admin",
	})
	require.NoError(t, err)
	const seedCount = 12
	for i := 0; i < seedCount; i++ {
		require.NoError(t, promptSvc.AddAnnotation(ctx, node.ID, fmt.Sprintf("seed-%d", i), "agent", ""))
	}
	seeded, err := nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)

	errs := runConcurrently(2*seedCount, func(i int) error {
		if i < seedCount {
			return promptSvc.ResolveAnnotation(ctx, node.ID, seeded.Annotations[i].ID, true)
		}
		return promptSvc.AddAnnotation(ctx, node.ID, fmt.Sprintf("new-%d", i), "agent", "")
	})
	for _, e := range errs {
		require.NoError(t, e)
	}

	got, err := nodeSvc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, 2*seedCount)
	resolved := 0
	for _, a := range got.Annotations {
		if a.Resolved {
			resolved++
		}
	}
	require.Equal(t, seedCount, resolved)
}

// TestNodeServiceAppendAnnotation_ConcurrentWriters_PersistsEveryAnnotation
// covers the HTTP append path, which shared the same lost-update pattern.
func TestNodeServiceAppendAnnotation_ConcurrentWriters_PersistsEveryAnnotation(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "HTTP race"})
	require.NoError(t, err)

	errs := runConcurrently(annotationRaceWriters, func(i int) error {
		return svc.AppendAnnotation(ctx, node.ID, model.Annotation{
			ID: fmt.Sprintf("ANN-%02d", i), Author: "agent", Text: fmt.Sprintf("t-%d", i),
		})
	})
	for _, e := range errs {
		require.NoError(t, e)
	}
	got, err := svc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.Len(t, got.Annotations, annotationRaceWriters)
}
