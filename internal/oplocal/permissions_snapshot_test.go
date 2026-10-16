// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_SnapshotCompletedWriterCannotABA(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	paused := make(chan struct{})
	resume := make(chan struct{})
	first := true
	s.permissionReadStage = func(string) {
		if first {
			first = false
			close(paused)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}
	}
	type result struct {
		snapshot permissionSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() { snapshot, err := s.permissionSnapshot(ctx); done <- result{snapshot, err} }()
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatal("reader did not pause")
	}
	batch.Changes[0].Action = "deny"
	err := New(s.env).ApplyPermissions(ctx, batch, nil)
	close(resume)
	got := <-done
	require.NoError(t, err)
	require.NoError(t, got.err)
	require.Len(t, got.snapshot.grants, 1)
	require.True(t, got.snapshot.grants[0].Denied)
	require.Len(t, got.snapshot.rows, 2)
	require.Equal(t, "deny", got.snapshot.rows[1].Action)
}
