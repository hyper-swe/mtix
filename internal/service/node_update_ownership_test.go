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
	"github.com/hyper-swe/mtix/internal/store"
)

func assigneeUpdate(v string) *store.NodeUpdate { return &store.NodeUpdate{Assignee: &v} }

func updateAs(actor string, force bool, reason string) context.Context {
	return service.WithUpdateIdentity(context.Background(), service.UpdateIdentity{Actor: actor, Force: force, Reason: reason})
}

// TestUpdateNode_AssigneeOwnership_RefusesNonAssignee verifies clearing or
// changing another agent's claim through update is refused (MTIX-325).
func TestUpdateNode_AssigneeOwnership_RefusesNonAssignee(t *testing.T) {
	for _, tc := range []struct{ name, ident, val string }{
		{"clear_by_other", "agent-b", ""},
		{"change_by_other", "agent-b", "agent-c"},
		{"case_variant", "Agent-A", ""},
		{"no_identity", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newTestNodeService(t)
			id := claimedTestNode(t, svc, "agent-a")
			ctx := context.Background()
			if tc.ident != "" {
				ctx = updateAs(tc.ident, false, "")
			}

			err := svc.UpdateNode(ctx, id, assigneeUpdate(tc.val))

			require.ErrorIs(t, err, model.ErrAlreadyClaimed)
			assert.Contains(t, err.Error(), "agent-a")
			assert.Contains(t, err.Error(), "--force")
			got, getErr := st.GetNode(context.Background(), id)
			require.NoError(t, getErr)
			assert.Equal(t, "agent-a", got.Assignee)
		})
	}
}

// TestUpdateNode_AssigneeOwnership_ApplyUpdateRefuses verifies the transport
// entry point ApplyUpdate shares the guard (MTIX-325).
func TestUpdateNode_AssigneeOwnership_ApplyUpdateRefuses(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	id := claimedTestNode(t, svc, "agent-a")
	err := svc.ApplyUpdate(updateAs("agent-b", false, ""), id, (*service.NodeUpdate)(assigneeUpdate("")))
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
}

// TestUpdateNode_AssigneeOwnership_AllowedCases verifies the assignee, a
// system actor and non-owner field edits keep working (MTIX-325).
func TestUpdateNode_AssigneeOwnership_AllowedCases(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	id := claimedTestNode(t, svc, "agent-a")
	title := "renamed"
	require.NoError(t, svc.UpdateNode(updateAs("agent-b", false, ""), id, &store.NodeUpdate{Title: &title}))
	same := "agent-a"
	require.NoError(t, svc.UpdateNode(updateAs("agent-b", false, ""), id, &store.NodeUpdate{Assignee: &same}))
	require.NoError(t, svc.UpdateNode(updateAs("agent-a", false, ""), id, assigneeUpdate("agent-c")))
	require.NoError(t, svc.UpdateNode(updateAs("agent-c", false, ""), id, assigneeUpdate("")))
	got, err := st.GetNode(context.Background(), id)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
	// Unclaimed nodes may be assigned by anyone; a system actor overrides.
	require.NoError(t, svc.UpdateNode(updateAs("agent-b", false, ""), id, assigneeUpdate("agent-d")))
	require.NoError(t, svc.UpdateNode(service.WithUpdateIdentity(context.Background(),
		service.UpdateIdentity{System: true}), id, assigneeUpdate("")))
	got, err = st.GetNode(context.Background(), id)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
}

// TestUpdateNode_ForceByNonAssignee_RecordsAudit verifies a forced clear or
// change succeeds and records actor, previous assignee and reason (MTIX-325).
func TestUpdateNode_ForceByNonAssignee_RecordsAudit(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	id := claimedTestNode(t, svc, "agent-a")

	require.NoError(t, svc.UpdateNode(updateAs("agent-b", true, "host crashed"), id, assigneeUpdate("")))

	got, err := st.GetNode(context.Background(), id)
	require.NoError(t, err)
	assert.Empty(t, got.Assignee)
	entry := lastUnclaimActivity(t, svc, id)
	assert.Equal(t, "agent-b", entry.Author)
	assert.Contains(t, entry.Text, "forced unclaim by agent-b of claim held by agent-a: host crashed")

	id2 := claimedTestNode(t, svc, "agent-a")
	require.NoError(t, svc.UpdateNode(updateAs("agent-b", true, "reassign"), id2, assigneeUpdate("agent-c")))
	entries, err := svc.GetActivity(context.Background(), id2, 100, 0)
	require.NoError(t, err)
	var text string
	for _, e := range entries {
		if e.Author == "agent-b" {
			text = e.Text
		}
	}
	assert.Contains(t, text, "forced assignee change by agent-b of claim held by agent-a to agent-c: reassign")
}

// TestUpdateNode_ForceWithoutReasonOrActor_Refused verifies force needs a
// reason and an acting identity (MTIX-325).
func TestUpdateNode_ForceWithoutReasonOrActor_Refused(t *testing.T) {
	svc, st, _ := newTestNodeService(t)
	id := claimedTestNode(t, svc, "agent-a")
	err := svc.UpdateNode(updateAs("agent-b", true, ""), id, assigneeUpdate(""))
	require.ErrorIs(t, err, model.ErrInvalidInput)
	got, getErr := st.GetNode(context.Background(), id)
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
}
