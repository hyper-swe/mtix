// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
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
