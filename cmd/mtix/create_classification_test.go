// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// CLI creation exposes distinct classification, assignment and authorship (MTIX-119).
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateCLI_ClassificationAndAssignment(t *testing.T) {
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", ""} {
		t.Run(kind, func(t *testing.T) {
			initTestApp(t)
			app.authorID = "author"
			app.jsonOutput = true
			output := captureStdout(t, func() {
				require.NoError(t, runCreate("Classified work", "", kind, 3, "", "context", "accepted", "", "worker"))
			})
			var n model.Node
			require.NoError(t, json.Unmarshal([]byte(output), &n))
			assert.Equal(t, model.IssueType(kind), n.IssueType)
			assert.Equal(t, "author", n.Creator)
			assert.Equal(t, "worker", n.Assignee)
			assert.Equal(t, model.StatusInProgress, n.Status)
			stored, err := app.store.GetNode(context.Background(), n.ID)
			require.NoError(t, err)
			assert.Equal(t, n.IssueType, stored.IssueType)
			app.jsonOutput = false
			shown := captureStdout(t, func() { require.NoError(t, runShow(n.ID)) })
			if kind != "" {
				assert.Contains(t, shown, "Issue type:")
				assert.Contains(t, shown, kind)
			}
		})
	}
}
func TestCreateCLI_InvalidIssueType_ReturnsAllowedList(t *testing.T) {
	initTestApp(t)
	err := runCreate("Bad classification", "", "epic", 3, "", "", "", "", "worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refactor")
}

func TestCreateCLI_HelpAndListClassification(t *testing.T) {
	initTestApp(t)
	app.authorID = "author"
	require.NoError(t, runCreate("Classified work", "", "feature", 3, "", "context", "accepted", "", "worker"))
	table := captureStdout(t, func() { require.NoError(t, runList("", "", "", "", "", "", "", "", 0, false, 50, "", false)) })
	assert.Contains(t, table, "Node type")
	assert.Contains(t, table, "Issue type")
	assert.Contains(t, table, "epic")
	assert.Contains(t, table, "feature")
	cmd := newCreateCmd()
	for _, kind := range []string{"bug", "feature", "task", "chore", "refactor", "test", "doc", "unset"} {
		assert.Contains(t, cmd.Flags().Lookup("type").Usage, kind)
	}
	assert.Contains(t, cmd.Flags().Lookup("assign").Usage, "atomically")
	assert.Contains(t, cmd.Flags().Lookup("assign").Usage, "creator stays author")
	app.jsonOutput = true
	raw := captureStdout(t, func() { require.NoError(t, runShow("TEST-1")) })
	var n model.Node
	require.NoError(t, json.Unmarshal([]byte(raw), &n))
	assert.Equal(t, model.IssueTypeFeature, n.IssueType)
	assert.Equal(t, model.NodeTypeEpic, n.NodeType)
	assert.Equal(t, "author", n.Creator)
	assert.Equal(t, "worker", n.Assignee)
}
