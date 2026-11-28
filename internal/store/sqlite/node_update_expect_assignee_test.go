// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestUpdateNode_ExpectAssignee_Mismatch_RefusesWithoutWrite verifies the
// compare-and-set inside the update transaction (MTIX-325.1).
func TestUpdateNode_ExpectAssignee_Mismatch_RefusesWithoutWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	node := makeRootNode("EA-1", "EA", "ea", time.Now().UTC().Truncate(time.Second))
	require.NoError(t, s.CreateNode(ctx, node))
	require.NoError(t, s.ClaimNode(ctx, "EA-1", "agent-a"))

	title := "changed"
	err := s.UpdateNode(ctx, "EA-1", &store.NodeUpdate{Title: &title, Assignee: strPtr(""), ExpectAssignee: strPtr("agent-z")})

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	got, getErr := s.GetNode(ctx, "EA-1")
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
	assert.NotEqual(t, title, got.Title)

	require.NoError(t, s.UpdateNode(ctx, "EA-1", &store.NodeUpdate{Assignee: strPtr(""), ExpectAssignee: strPtr("agent-a")}))
	require.NoError(t, s.UpdateNode(ctx, "EA-1", &store.NodeUpdate{Assignee: strPtr("agent-b"), ExpectAssignee: strPtr("")}))
}

// TestUnclaimNodeExpecting_Mismatch_RefusesWithoutWrite verifies the unclaim
// compare-and-set (MTIX-325.1).
func TestUnclaimNodeExpecting_Mismatch_RefusesWithoutWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, makeRootNode("EA-2", "EA", "ea", time.Now().UTC().Truncate(time.Second))))
	require.NoError(t, s.ClaimNode(ctx, "EA-2", "agent-a"))

	err := s.UnclaimNodeExpecting(ctx, "EA-2", "release", "agent-b", "agent-z")

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	got, getErr := s.GetNode(ctx, "EA-2")
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
	assert.Equal(t, model.StatusInProgress, got.Status)
	require.NoError(t, s.UnclaimNodeExpecting(ctx, "EA-2", "release", "agent-a", "agent-a"))
}
