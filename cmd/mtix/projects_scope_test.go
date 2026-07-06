// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	<-done
	os.Stdout = orig
	return buf.String()
}

// seedTwoProjects creates root nodes in the primary project (TEST, per
// initTestApp) and a second project (OTHER), returning their counts.
// It seeds 2 TEST roots and 1 OTHER root.
func seedTwoProjects(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	seed := func(project, title string) {
		_, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: project,
			Title:   title,
			Creator: "tester",
		})
		require.NoError(t, err)
	}
	seed("TEST", "Primary one")
	seed("TEST", "Primary two")
	seed("OTHER", "Other one")
}

// listJSON runs the list command in JSON mode and returns the node IDs.
func listJSON(t *testing.T, project string, allProjects bool) []string {
	t.Helper()
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() {
		require.NoError(t, runList("", "", "", "", "", "", "", 0, false, 50, project, allProjects))
	})
	return extractNodeIDs(t, out)
}

func extractNodeIDs(t *testing.T, out string) []string {
	t.Helper()
	var payload struct {
		Nodes []struct {
			ID      string `json:"id"`
			Project string `json:"project"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	ids := make([]string, 0, len(payload.Nodes))
	for _, n := range payload.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func projectsOf(ids []string) map[string]bool {
	prefixes := map[string]bool{}
	for _, id := range ids {
		// id is "<PREFIX>-<n>"; take the prefix before the first '-'.
		for i := 0; i < len(id); i++ {
			if id[i] == '-' {
				prefixes[id[:i]] = true
				break
			}
		}
	}
	return prefixes
}

// TestRunList_DefaultScope_PrimaryOnly verifies list defaults to the primary
// project (MP-7).
func TestRunList_DefaultScope_PrimaryOnly(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)

	ids := listJSON(t, "", false)
	require.Len(t, ids, 2)
	assert.Equal(t, map[string]bool{"TEST": true}, projectsOf(ids))
}

// TestRunList_ProjectFlag_OtherOnly verifies --project scopes to one project.
func TestRunList_ProjectFlag_OtherOnly(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)

	ids := listJSON(t, "OTHER", false)
	require.Len(t, ids, 1)
	assert.Equal(t, map[string]bool{"OTHER": true}, projectsOf(ids))
}

// TestRunList_AllProjects_ReturnsAll verifies --all-projects spans projects.
func TestRunList_AllProjects_ReturnsAll(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)

	ids := listJSON(t, "", true)
	require.Len(t, ids, 3)
	assert.Equal(t, map[string]bool{"TEST": true, "OTHER": true}, projectsOf(ids))
}

// TestRunList_ProjectAndAllProjects_Conflict verifies mutual exclusivity.
func TestRunList_ProjectAndAllProjects_Conflict(t *testing.T) {
	initTestApp(t)
	err := runList("", "", "", "", "", "", "", 0, false, 50, "OTHER", true)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestRunSearch_DefaultScope_PrimaryOnly verifies search defaults to primary.
func TestRunSearch_DefaultScope_PrimaryOnly(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() {
		require.NoError(t, runSearch("", "", "", "", "", "", 50, "", false))
	})
	ids := extractNodeIDs(t, out)
	require.Len(t, ids, 2)
	assert.Equal(t, map[string]bool{"TEST": true}, projectsOf(ids))
}

// TestRunSearch_ProjectFlag_OtherOnly verifies search --project scoping.
func TestRunSearch_ProjectFlag_OtherOnly(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() {
		require.NoError(t, runSearch("", "", "", "", "", "", 50, "OTHER", false))
	})
	ids := extractNodeIDs(t, out)
	require.Len(t, ids, 1)
	assert.Equal(t, map[string]bool{"OTHER": true}, projectsOf(ids))
}

// TestRunSearch_AllProjects_ReturnsAll verifies search --all-projects.
func TestRunSearch_AllProjects_ReturnsAll(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() {
		require.NoError(t, runSearch("", "", "", "", "", "", 50, "", true))
	})
	ids := extractNodeIDs(t, out)
	require.Len(t, ids, 3)
}

// TestRunProjects_JSON_ListsDistinctWithCountsAndPrimary verifies MP-8 output.
func TestRunProjects_JSON_ListsDistinctWithCountsAndPrimary(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() {
		require.NoError(t, runProjects())
	})

	var payload struct {
		Projects []struct {
			Prefix    string `json:"prefix"`
			Count     int    `json:"count"`
			IsPrimary bool   `json:"is_primary"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))

	counts := map[string]int{}
	primary := map[string]bool{}
	for _, p := range payload.Projects {
		counts[p.Prefix] = p.Count
		primary[p.Prefix] = p.IsPrimary
	}
	assert.Equal(t, 2, counts["TEST"])
	assert.Equal(t, 1, counts["OTHER"])
	assert.True(t, primary["TEST"], "TEST is the configured primary")
	assert.False(t, primary["OTHER"], "OTHER is not primary")
}

// TestRunProjects_Human_MarksPrimary verifies the human table marks the primary.
func TestRunProjects_Human_MarksPrimary(t *testing.T) {
	initTestApp(t)
	seedTwoProjects(t)

	out := captureStdout(t, func() {
		require.NoError(t, runProjects())
	})
	assert.Contains(t, out, "TEST")
	assert.Contains(t, out, "OTHER")
	assert.Contains(t, out, "*") // primary marker
}
