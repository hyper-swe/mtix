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

// initUnclaimTestApp starts a project as identity (empty for none) with
// TEST-1 claimed by agent-a (MTIX-169).
func initUnclaimTestApp(t *testing.T, identity string) {
	t.Helper()
	t.Setenv("MTIX_AUTHOR_ID", identity)
	initTestApp(t)
	require.NoError(t, runCreate("Claimed work", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runClaim("TEST-1", "agent-a"))
}

// executeUnclaim runs mtix unclaim TEST-1 with args through Cobra.
func executeUnclaim(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newUnclaimCmd()
	cmd.SetArgs(append([]string{"TEST-1", "--reason", "handoff"}, args...))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

// TestUnclaimCmd_NonAssigneeIdentity_RefusesWithAssigneeAndForce verifies an
// agent cannot release another agent's claim from the CLI (MTIX-169).
func TestUnclaimCmd_NonAssigneeIdentity_RefusesWithAssigneeAndForce(t *testing.T) {
	initUnclaimTestApp(t, "agent-b")

	err := executeUnclaim(t)

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Contains(t, err.Error(), "agent-a")
	assert.Contains(t, err.Error(), "--force")
	got, getErr := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
}

// TestUnclaimCmd_NoIdentity_RefusesWithGuidance verifies an unclaim without
// any resolvable agent identity is refused (MTIX-169).
func TestUnclaimCmd_NoIdentity_RefusesWithGuidance(t *testing.T) {
	initUnclaimTestApp(t, "")

	err := executeUnclaim(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--agent")
	assert.Contains(t, err.Error(), "MTIX_AUTHOR_ID")
	got, getErr := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, getErr)
	assert.Equal(t, "agent-a", got.Assignee)
}

// TestUnclaimCmd_AssigneeIdentity_RecordsRealActor verifies the assignee's own
// unclaim succeeds and records the agent, not the literal cli (MTIX-169).
func TestUnclaimCmd_AssigneeIdentity_RecordsRealActor(t *testing.T) {
	initUnclaimTestApp(t, "agent-a")

	require.NoError(t, executeUnclaim(t))

	assert.Equal(t, "agent-a", lastCLIUnclaimEntry(t).Author)
}

// lastCLIUnclaimEntry returns the newest unclaim activity entry on TEST-1.
func lastCLIUnclaimEntry(t *testing.T) model.ActivityEntry {
	t.Helper()
	entries, err := app.store.GetActivity(context.Background(), "TEST-1", 100, 0)
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
