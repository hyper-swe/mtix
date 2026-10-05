// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Atomic explicit assignment shares normal claim activity/agent/event writes.
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type atomicNodeCreator interface {
	CreateNodeAndClaim(context.Context, *model.Node, string) error
}

func TestCreateNodeAndClaim_AtomicStoreBoundary(t *testing.T) {
	s, _ := applyTestStore(t)
	creator, ok := any(s).(atomicNodeCreator)
	require.True(t, ok, "store must expose atomic create-and-claim")
	node := &model.Node{ID: "TEST-1", Project: "TEST", Title: "Assigned work", Status: model.StatusOpen, NodeType: model.NodeTypeEpic, Priority: model.PriorityMedium, Weight: 1, CreatedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	node.Project = "TEST"
	node.Creator = "author"
	require.NoError(t, creator.CreateNodeAndClaim(context.Background(), node, "worker"))
	got, err := s.GetNode(context.Background(), node.ID)
	require.NoError(t, err)
	assert.Equal(t, "author", got.Creator)
	assert.Equal(t, "worker", got.Assignee)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

func TestCreateNodeAndClaim_InvalidInitialState_WritesNothing(t *testing.T) {
	tests := []struct {
		name               string
		status             model.Status
		assigned, assignee string
	}{
		{"missing assignee", model.StatusOpen, "", ""},
		{"already assigned", model.StatusOpen, "old-worker", "worker"},
		{"already in progress", model.StatusInProgress, "", "worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
			n := &model.Node{ID: "TEST-1", Project: "TEST", Title: "Invalid initial state", NodeType: model.NodeTypeEpic, Status: tt.status, Assignee: tt.assigned, Priority: model.PriorityMedium, Weight: 1, CreatedAt: now, UpdatedAt: now}
			require.ErrorIs(t, s.CreateNodeAndClaim(context.Background(), n, tt.assignee), model.ErrInvalidInput)
			assert.Zero(t, countNodes(t, raw))
			assert.Zero(t, countEvents(t, raw))
		})
	}
}
