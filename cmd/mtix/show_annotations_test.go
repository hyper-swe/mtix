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

// labelPattern matches the label of every top-level line show prints.
var labelPattern = regexp.MustCompile(`(?m)^([A-Z][A-Za-z]*):`)

func TestShowHelp_DescribesEveryLabelTheCommandPrints(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	// Every optional field is set so show prints every label it can print.
	node, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project:     "TEST",
		Title:       "Fully populated",
		Description: "A description",
		Prompt:      "A prompt",
		Creator:     "tester",
	})
	require.NoError(t, err)
	id := node.ID
	require.NoError(t, app.store.ClaimNode(ctx, id, "agent-x"))
	captureStdout(t, func() { require.NoError(t, runAnnotate(id, "note")) })

	out := showOutput(t, id)
	help := newShowCmd().Long

	labels := labelPattern.FindAllStringSubmatch(out, -1)
	require.NotEmpty(t, labels)
	for _, m := range labels {
		assert.Contains(t, help, m[1], "help text must describe the %q line", m[1])
	}
	assert.Contains(t, help, "--json", "help must say where the complete record is")
}

func TestShowParity_MCPShowReturnsSameAnnotationsAsCLI(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "A description")
	for _, text := range []string{"first verdict", "second verdict"} {
		captureStdout(t, func() { require.NoError(t, runAnnotate(id, text)) })
	}

	cliOut := showOutput(t, id)

	reg := mcp.NewToolRegistry()
	mcp.RegisterNodeTools(reg, app.nodeSvc, app.store)
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
