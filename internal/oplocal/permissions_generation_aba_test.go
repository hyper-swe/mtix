// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"time"

	"github.com/stretchr/testify/require"
	"testing"
)

// This control qualifies expiration synthetically, not as production eviction proof.
func TestPermissions_GenerationLiteralABASnapshotCoherent(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	first := generationPending(t, s)
	synthesizeExpiredCompletedPermissionHistory(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	paused, resume, done := pauseExpiredMarkerReader(ctx, s)
	released, joined := false, false
	defer func() {
		if !released {
			close(resume)
		}
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-paused:
	case early := <-done:
		joined = true
		t.Fatalf("reader ended before pause: %v", early.err)
	case <-ctx.Done():
		t.Fatal("reader did not pause")
	}
	completed, writerErr := writeLiteralGenerationBA(ctx, s, intent)
	close(resume)
	released = true
	got := <-done
	joined = true
	require.NoError(t, writerErr)
	assertLiteralGenerationABA(t, intent, first, completed, got)
}
func writeLiteralGenerationBA(ctx context.Context, s *State, intent PermissionBatch) ([]permissionPending, error) {
	writer := New(s.env)
	completed := []permissionPending{}
	for i, id := range []string{"22222222222222222222222222222222", intent.ID} {
		next := intent
		next.ID = id
		next.Changes = []PermissionChange{{AgentID: "fixture-agent", Action: "deny"}}
		observed := alertTime().Add(time.Duration(i+1) * time.Hour)
		prepare := func(_ context.Context, b PermissionBatch) (PermissionBatch, error) { b.Time = observed; return b, nil }
		if err := writer.ApplyPermissionIntentScoped(ctx, next.BoardID, next, prepare, nil); err != nil {
			return nil, err
		}
		var p permissionPending
		if _, err := writer.ReadJSON("permissions-pending", &p); err != nil {
			return nil, err
		}
		completed = append(completed, p)
	}
	return completed, nil
}

func assertLiteralGenerationABA(t *testing.T, intent PermissionBatch, first permissionPending, completed []permissionPending, got expiredMarkerSnapshot) {
	t.Helper()
	require.NoError(t, got.err)
	require.Len(t, completed, 2)
	require.Equal(t, intent.ID, first.CompletedID)
	require.Equal(t, "22222222222222222222222222222222", completed[0].CompletedID)
	require.Equal(t, intent.ID, completed[1].CompletedID)
	generations := map[string]bool{}
	for _, p := range append([]permissionPending{first}, completed...) {
		require.True(t, permissionID(p.Generation))
		require.False(t, generations[p.Generation])
		generations[p.Generation] = true
	}
	require.Len(t, got.snapshot.grants, 1)
	require.True(t, got.snapshot.grants[0].Denied)
	require.Len(t, got.snapshot.rows, 2)
	require.Equal(t, "deny", got.snapshot.rows[0].Action)
	require.Equal(t, "deny", got.snapshot.rows[1].Action)
	require.Equal(t, intent.ID, got.snapshot.rows[1].ID)
	require.Equal(t, alertTime().Add(2*time.Hour), got.snapshot.rows[1].Time)
}
