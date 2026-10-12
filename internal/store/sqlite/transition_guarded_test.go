// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-151: guarded transitions decide inside the write transaction.

func TestTransitionStatusFrom_Cases_GuardsOnExpectedStatus(t *testing.T) {
	tests := []struct {
		name     string
		start    model.Status
		expected model.Status
		to       model.Status
		id       string
		wantErr  error
		want     model.Status
	}{
		{"matching status transitions", model.StatusOpen, model.StatusOpen, model.StatusInProgress, "PROJ-1", nil, model.StatusInProgress},
		{"changed status is a conflict", model.StatusCancelled, model.StatusOpen, model.StatusInProgress, "PROJ-1", model.ErrConflict, model.StatusCancelled},
		{"matching but invalid transition", model.StatusOpen, model.StatusOpen, model.StatusDone, "PROJ-1", model.ErrInvalidTransition, model.StatusOpen},
		{"missing node", model.StatusOpen, model.StatusOpen, model.StatusInProgress, "PROJ-404", model.ErrNotFound, model.StatusOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, tt.start)
			ctx := context.Background()
			err := s.TransitionStatusFrom(ctx, tt.id, tt.expected, tt.to, "r", "admin")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			got, gerr := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, gerr)
			assert.Equal(t, tt.want, got.Status)
		})
	}
}

func TestRestoreInvalidated_Cases_DecidesTargetInTransaction(t *testing.T) {
	tests := []struct {
		name    string
		prep    []model.Status
		id      string
		wantErr error
		want    model.Status
	}{
		{"back to previous status", []model.Status{model.StatusInProgress, model.StatusInvalidated}, "PROJ-1", nil, model.StatusInProgress},
		{"empty previous falls back to open", []model.Status{model.StatusInvalidated}, "PROJ-1", nil, model.StatusOpen},
		{"unreachable previous falls back to open", []model.Status{model.StatusInProgress, model.StatusDone, model.StatusInvalidated}, "PROJ-1", nil, model.StatusOpen},
		{"not invalidated is rejected", nil, "PROJ-1", model.ErrInvalidTransition, model.StatusOpen},
		{"missing node", nil, "PROJ-404", model.ErrNotFound, model.StatusOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()
			for _, st := range tt.prep {
				require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", st, "prep", "admin"))
			}
			to, err := s.RestoreInvalidated(ctx, tt.id, "admin")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, to)
			}
			got, gerr := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, gerr)
			assert.Equal(t, tt.want, got.Status)
		})
	}
}

// TestRestoreInvalidated_SoftDeletedNode_ReturnsNotFound pins that a
// soft-deleted invalidated node is not restored, nor found (F7).
func TestRestoreInvalidated_SoftDeletedNode_ReturnsNotFound(t *testing.T) {
	s := newDeferTestStore(t, model.StatusOpen)
	ctx := context.Background()
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusInvalidated, "stale", "admin"))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	_, err := s.RestoreInvalidated(ctx, "PROJ-1", "admin")
	require.ErrorIs(t, err, model.ErrNotFound)
	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1"))
	got, gerr := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, gerr)
	assert.Equal(t, model.StatusInvalidated, got.Status)
}

// TestRestoreInvalidated_ConcurrentReinvalidation_TargetMatchesItsInvalidation
// runs restores against a writer that keeps re-invalidating the node from
// different statuses. Every restore must land on the previous_status of the
// invalidation it consumed, which only holds if previous_status is read in the
// restore's own write transaction (F5).
func TestRestoreInvalidated_ConcurrentReinvalidation_TargetMatchesItsInvalidation(t *testing.T) {
	s := newDeferTestStore(t, model.StatusOpen)
	ctx := context.Background()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			n, err := s.GetNode(ctx, "PROJ-1")
			if err != nil {
				return
			}
			switch {
			case n.Status == model.StatusOpen && i%2 == 0:
				_ = s.ClaimNode(ctx, "PROJ-1", "agent-1")
			case n.Status == model.StatusOpen || n.Status == model.StatusInProgress:
				_ = s.TransitionStatus(ctx, "PROJ-1", model.StatusInvalidated, "stale", "system")
			}
		}
	}()
	restores := 0
	for i := 0; i < 1500; i++ {
		if _, err := s.RestoreInvalidated(ctx, "PROJ-1", "admin"); err == nil {
			restores++
		} else {
			require.ErrorIs(t, err, model.ErrInvalidTransition)
		}
	}
	close(stop)
	<-done
	require.Positive(t, restores)

	entries, err := s.GetActivity(ctx, "PROJ-1", 100000, 0)
	require.NoError(t, err)
	checked := 0
	var invalidatedFrom string
	for _, e := range entries {
		if e.Type != model.ActivityTypeStatusChange {
			continue
		}
		var m map[string]string
		require.NoError(t, json.Unmarshal(e.Metadata, &m))
		switch {
		case m["to_status"] == string(model.StatusInvalidated):
			invalidatedFrom = m["from_status"]
		case m["from_status"] == string(model.StatusInvalidated) && e.Text == "restored from invalidated":
			assert.Equal(t, invalidatedFrom, m["to_status"], "restore must return to the status it was invalidated from")
			checked++
		}
	}
	assert.Equal(t, restores, checked)
}
