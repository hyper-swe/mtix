// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// executeUpdateAssignee runs mtix update TEST-1 --assignee "" with args.
func executeUpdateAssignee(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newUpdateCmd()
	cmd.SetArgs(append([]string{"TEST-1", "--assignee", ""}, args...))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

func testNodeAssignee(t *testing.T) string {
	t.Helper()
	got, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	return got.Assignee
}

// TestUpdateCmd_AssigneeClear_NonAssigneeRefused verifies update cannot drop
// another agent's claim, naming the assignee and --force (MTIX-325).
func TestUpdateCmd_AssigneeClear_NonAssigneeRefused(t *testing.T) {
	initUnclaimTestApp(t, "agent-b")

	err := executeUpdateAssignee(t)

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Contains(t, err.Error(), "agent-a")
	assert.Contains(t, err.Error(), "--force")
	assert.Equal(t, "agent-a", testNodeAssignee(t))
}

// TestUpdateCmd_AssigneeChange_NonAssigneeRefused covers a reassignment.
func TestUpdateCmd_AssigneeChange_NonAssigneeRefused(t *testing.T) {
	initUnclaimTestApp(t, "agent-b")
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"TEST-1", "--assignee", "agent-c"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true

	require.ErrorIs(t, cmd.Execute(), model.ErrAlreadyClaimed)
	assert.Equal(t, "agent-a", testNodeAssignee(t))
}

// TestUpdateCmd_AssigneeClear_NoIdentityRefused verifies no resolvable
// identity cannot bypass the check (MTIX-325).
func TestUpdateCmd_AssigneeClear_NoIdentityRefused(t *testing.T) {
	initUnclaimTestApp(t, "")

	require.ErrorIs(t, executeUpdateAssignee(t), model.ErrAlreadyClaimed)
	assert.Equal(t, "agent-a", testNodeAssignee(t))
}

// TestUpdateCmd_AssigneeClear_Force_RecordsAudit verifies --force --reason
// releases the claim and records actor, previous assignee and reason.
func TestUpdateCmd_AssigneeClear_Force_RecordsAudit(t *testing.T) {
	initUnclaimTestApp(t, "agent-b")

	require.Error(t, executeUpdateAssignee(t, "--force"), "force needs a reason")
	require.NoError(t, executeUpdateAssignee(t, "--force", "--reason", "host crashed"))

	assert.Empty(t, testNodeAssignee(t))
	entry := lastCLIUnclaimEntry(t)
	assert.Equal(t, "agent-b", entry.Author)
	assert.Contains(t, entry.Text, "forced unclaim by agent-b of claim held by agent-a: host crashed")
}

// TestUpdateCmd_AssigneeClear_OwnerAllowed verifies the assignee's own update.
func TestUpdateCmd_AssigneeClear_OwnerAllowed(t *testing.T) {
	initUnclaimTestApp(t, "agent-a")

	require.NoError(t, executeUpdateAssignee(t))
	assert.Empty(t, testNodeAssignee(t))
}

// TestUpdateCmd_AssigneeClear_AgentFlagOverridesIdentity verifies --agent names the actor.
func TestUpdateCmd_AssigneeClear_AgentFlagOverridesIdentity(t *testing.T) {
	initUnclaimTestApp(t, "agent-b")

	require.NoError(t, executeUpdateAssignee(t, "--agent", "agent-a"))
	assert.Empty(t, testNodeAssignee(t))
}
