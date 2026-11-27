// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func progressCLIChild(t *testing.T, title, parent string) string {
	t.Helper()
	n, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{
		Title: title, ParentID: parent, Project: "TEST", Creator: "tester"})
	require.NoError(t, err)
	return n.ID
}

// TestRunProgress_JSONExclusionMetadata covers FR-5.6a and FR-5.6b on the CLI.
func TestRunProgress_JSONExclusionMetadata(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	root := progressCLIChild(t, "cli root", "")
	a := progressCLIChild(t, "cli a", root)
	grand := progressCLIChild(t, "cli grand", a)
	require.NoError(t, app.store.TransitionStatus(t.Context(), grand, model.StatusInvalidated, "r", "t"))

	var got map[string]any
	out := captureStdout(t, func() { require.NoError(t, runProgress(root)) })
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, float64(1), got["invalidated_count"])
	assert.Equal(t, true, got["has_invalidated_descendants"])
	assert.Equal(t, false, got["all_children_excluded"])

	require.NoError(t, app.store.TransitionStatus(t.Context(), a, model.StatusInvalidated, "r", "t"))
	out = captureStdout(t, func() { require.NoError(t, runProgress(root)) })
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, true, got["all_children_excluded"])
}

// TestRunProgress_HumanWarnsAboutExclusions covers the CLI wording of FR-5.6a/b.
func TestRunProgress_HumanWarnsAboutExclusions(t *testing.T) {
	initTestApp(t)
	root := progressCLIChild(t, "cli root", "")
	a := progressCLIChild(t, "cli a", root)
	require.NoError(t, app.store.TransitionStatus(t.Context(), a, model.StatusInvalidated, "r", "t"))

	out := captureStdout(t, func() { require.NoError(t, runProgress(root)) })
	assert.Contains(t, out, "all 1 children excluded")
	assert.Contains(t, out, "1 invalidated descendants")
}
