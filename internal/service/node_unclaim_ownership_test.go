// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// claimedTestNode creates a node claimed by agent and returns its ID (MTIX-169).
func claimedTestNode(t *testing.T, svc *service.NodeService, agent string) string {
	t.Helper()
	ctx := context.Background()
	n, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "claimed work"})
	require.NoError(t, err)
	require.NoError(t, svc.ClaimNode(ctx, n.ID, agent))
	return n.ID
}

// TestUnclaimNode_NonAssignee_ReturnsErrAlreadyClaimed verifies another agent
// cannot release a claim it does not hold (MTIX-169).
func TestUnclaimNode_NonAssignee_ReturnsErrAlreadyClaimed(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	id := claimedTestNode(t, svc, "agent-a")

	err := svc.UnclaimNode(ctx, id, "taking over", "agent-b", false)

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Contains(t, err.Error(), "agent-a")
	assert.Contains(t, err.Error(), "--force")
	got, getErr := st.GetNode(ctx, id)
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestUnclaimNode_EmptyActor_ReturnsErrInvalidInput verifies an unclaim
// without an acting agent identity is refused (MTIX-169).
func TestUnclaimNode_EmptyActor_ReturnsErrInvalidInput(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	id := claimedTestNode(t, svc, "agent-a")

	err := svc.UnclaimNode(ctx, id, "release", "", false)

	require.ErrorIs(t, err, model.ErrInvalidInput)
	got, getErr := st.GetNode(ctx, id)
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
}

// TestUnclaimNode_Assignee_RecordsActor verifies the assignee's own unclaim
// succeeds and records the assignee as the actor (MTIX-169).
func TestUnclaimNode_Assignee_RecordsActor(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	ctx := context.Background()
	id := claimedTestNode(t, svc, "agent-a")

	require.NoError(t, svc.UnclaimNode(ctx, id, "handing back", "agent-a", false))

	got, err := st.GetNode(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
	entry := lastUnclaimActivity(t, svc, id)
	assert.Equal(t, "agent-a", entry.Author)
	assert.Equal(t, "handing back", entry.Text)
}

// lastUnclaimActivity returns the newest unclaim activity entry for id.
func lastUnclaimActivity(t *testing.T, svc *service.NodeService, id string) model.ActivityEntry {
	t.Helper()
	entries, err := svc.GetActivity(context.Background(), id, 100, 0)
	require.NoError(t, err)
	var found *model.ActivityEntry
	for i := range entries {
		if entries[i].Type == model.ActivityTypeUnclaim {
			found = &entries[i]
		}
	}
	require.NotNil(t, found, "no unclaim activity entry")
	return *found
}

// TestUnclaimNode_ForceByNonAssignee_RecordsAuditEntry verifies a forced
// unclaim releases the claim and names the actor, the previous assignee and
// the reason in the activity log (MTIX-169).
func TestUnclaimNode_ForceByNonAssignee_RecordsAuditEntry(t *testing.T) {
	svc, st, bc := newTestNodeService(t)
	ctx := context.Background()
	id := claimedTestNode(t, svc, "agent-a")
	bc.Reset()

	require.NoError(t, svc.UnclaimNode(ctx, id, "host crashed mid-task", "agent-b", true))

	got, err := st.GetNode(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
	assert.Equal(t, model.StatusOpen, got.Status)
	entry := lastUnclaimActivity(t, svc, id)
	assert.Equal(t, "agent-b", entry.Author)
	assert.Contains(t, entry.Text,
		"forced unclaim by agent-b of claim held by agent-a: host crashed mid-task")
	assert.Equal(t, "agent-b", bc.Events()[0].Author)
}

// TestUnclaimNode_RefusalCases_ReturnSentinel verifies the remaining refusals
// keep their sentinels and leave the claim in place (MTIX-169).
func TestUnclaimNode_RefusalCases_ReturnSentinel(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		actor  string
		force  bool
		want   error
	}{
		{"empty_actor_with_force", "release", "", true, model.ErrInvalidInput},
		{"empty_reason_with_force", "", "agent-b", true, model.ErrInvalidInput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			ctx := context.Background()
			id := claimedTestNode(t, svc, "agent-a")

			require.ErrorIs(t, svc.UnclaimNode(ctx, id, tc.reason, tc.actor, tc.force), tc.want)

			got, err := st.GetNode(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, "agent-a", got.Assignee)
		})
	}
}
