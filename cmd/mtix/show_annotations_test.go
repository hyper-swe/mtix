// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// MTIX-98: `mtix show` must print every annotation of a node, oldest first,
// each with its author and timestamp, and must state absence explicitly.

// createShowNode creates a root node with a description and returns its id.
func createShowNode(t *testing.T, description string) string {
	t.Helper()
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project:     "TEST",
		Title:       "Show target",
		Description: description,
		Creator:     "tester",
	})
	require.NoError(t, err)
	return node.ID
}

// showOutput runs `mtix show <id>` in human mode and returns what it printed.
func showOutput(t *testing.T, id string) string {
	t.Helper()
	app.jsonOutput = false
	return captureStdout(t, func() {
		require.NoError(t, runShow(id))
	})
}

func TestRunShow_WithAnnotations_PrintsAllOldestFirstWithAuthorAndTimestamp(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")

	older := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 2, 14, 5, 0, 0, time.UTC)
	// Stored newest-first on purpose: show must order by time, not storage.
	require.NoError(t, app.store.SetAnnotations(context.Background(), id, []model.Annotation{
		{ID: "01K0000000000000000000000B", Author: "reviewer-b", Text: "FAIL: missing tests", CreatedAt: newer},
		{ID: "01K0000000000000000000000A", Author: "reviewer-a", Text: "PASS: looks good", CreatedAt: older},
	}))

	out := showOutput(t, id)

	iDesc := strings.Index(out, "Desc:")
	iAnn := strings.Index(out, "Annotations")
	iOlder := strings.Index(out, "2026-09-01T09:30:00Z")
	iNewer := strings.Index(out, "2026-09-02T14:05:00Z")
	require.NotEqual(t, -1, iAnn, "show must print an Annotations section:\n%s", out)
	require.NotEqual(t, -1, iOlder, "older annotation timestamp missing:\n%s", out)
	require.NotEqual(t, -1, iNewer, "newer annotation timestamp missing:\n%s", out)
	assert.Less(t, iDesc, iAnn, "annotations come after the description")
	assert.Less(t, iOlder, iNewer, "annotations are printed oldest first")
	assert.Contains(t, out, "reviewer-a")
	assert.Contains(t, out, "reviewer-b")
	assert.Contains(t, out, "PASS: looks good")
	assert.Contains(t, out, "FAIL: missing tests")
}

func TestRunShow_NoAnnotations_PrintsAnnotationsNone(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")

	out := showOutput(t, id)

	assert.Contains(t, out, "Annotations: none", "absence must be stated, never silent")
}

func TestRunShow_MultilineAndResolvedAnnotation_IndentsTextAndMarksResolved(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")
	require.NoError(t, app.store.SetAnnotations(context.Background(), id, []model.Annotation{{
		ID:        "01K0000000000000000000000C",
		Author:    "reviewer-c",
		Text:      "FAIL: two problems\nfirst problem\nsecond problem",
		CreatedAt: time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC),
		Resolved:  true,
		Addressee: "agent-x",
	}}))

	out := showOutput(t, id)

	assert.Contains(t, out, "(resolved)")
	assert.Contains(t, out, "agent-x")
	for _, line := range []string{"first problem", "second problem"} {
		assert.Regexp(t, regexp.MustCompile(`(?m)^\s+`+line+`$`), out,
			"continuation lines are indented under their annotation")
	}
}

func TestAnnotateThenShow_RoundTrip_ShowsNewAnnotation(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")

	captureStdout(t, func() {
		require.NoError(t, runAnnotate(id, "round-trip verdict"))
	})
	out := showOutput(t, id)

	assert.Contains(t, out, "round-trip verdict", "a successful annotate must be visible in show")
	assert.Contains(t, out, app.authorID)
	assert.NotContains(t, out, "Annotations: none")
}

func TestShowParity_MCPShowReturnsSameAnnotationsAsCLI(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")
	for _, text := range []string{"first verdict", "second verdict"} {
		captureStdout(t, func() { require.NoError(t, runAnnotate(id, text)) })
	}

	cliOut := showOutput(t, id)

	reg := mcp.NewToolRegistry()
	mcp.RegisterNodeTools(reg, app.nodeSvc)
	args, err := json.Marshal(map[string]string{"id": id})
	require.NoError(t, err)
	res, err := reg.Call(context.Background(), "mtix_show", args)
	require.NoError(t, err)
	require.False(t, res.IsError)
	var shown struct {
		Annotations []model.Annotation `json:"annotations"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &shown))

	require.Len(t, shown.Annotations, 2, "MCP show returns every annotation")
	last := -1
	for _, a := range shown.Annotations {
		pos := strings.Index(cliOut, a.Text)
		require.NotEqual(t, -1, pos, "CLI must print the annotation MCP returns: %q\n%s", a.Text, cliOut)
		assert.Greater(t, pos, last, "CLI and MCP present annotations in the same order")
		last = pos
		assert.Contains(t, cliOut, a.Author)
		assert.Contains(t, cliOut, a.CreatedAt.UTC().Format(time.RFC3339))
	}
}

func TestShowParity_OutOfOrderStoredAnnotations_SameOrderInCLIJSONAndMCP(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")

	older := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 2, 14, 5, 0, 0, time.UTC)
	// Stored newest-first, with two equal timestamps whose ids are out of order.
	require.NoError(t, app.store.SetAnnotations(context.Background(), id, []model.Annotation{
		{ID: "01K0000000000000000000000D", Author: "r", Text: "text-d", CreatedAt: newer},
		{ID: "01K0000000000000000000000C", Author: "r", Text: "text-c", CreatedAt: older},
		{ID: "01K0000000000000000000000B", Author: "r", Text: "text-b", CreatedAt: older},
		{ID: "01K0000000000000000000000A", Author: "r", Text: "text-a", CreatedAt: older.Add(-time.Hour)},
	}))
	want := []string{"text-a", "text-b", "text-c", "text-d"}

	cliOut := showOutput(t, id)
	last := -1
	for _, text := range want {
		pos := strings.Index(cliOut, text)
		require.NotEqual(t, -1, pos, "CLI prints %q\n%s", text, cliOut)
		assert.Greater(t, pos, last, "CLI order")
		last = pos
	}

	texts := func(as []model.Annotation) []string {
		out := make([]string, 0, len(as))
		for _, a := range as {
			out = append(out, a.Text)
		}
		return out
	}

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	jsonOut := captureStdout(t, func() { require.NoError(t, runShow(id)) })
	var fromJSON struct {
		Annotations []model.Annotation `json:"annotations"`
	}
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &fromJSON))
	assert.Equal(t, want, texts(fromJSON.Annotations), "--json order")

	reg := mcp.NewToolRegistry()
	mcp.RegisterNodeTools(reg, app.nodeSvc)
	args, err := json.Marshal(map[string]string{"id": id})
	require.NoError(t, err)
	res, err := reg.Call(context.Background(), "mtix_show", args)
	require.NoError(t, err)
	var fromMCP struct {
		Annotations []model.Annotation `json:"annotations"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &fromMCP))
	assert.Equal(t, want, texts(fromMCP.Annotations), "MCP order")
}
