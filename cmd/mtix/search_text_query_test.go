// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// seedSearchCorpus creates three nodes whose text lives in different fields.
func seedSearchCorpus(t *testing.T) {
	t.Helper()
	for _, req := range []*service.CreateNodeRequest{
		{Project: "TEST", Title: "Alpha template cleanup", Creator: "tester"},
		{Project: "TEST", Title: "Beta", Description: "mentions the Gizmo here", Creator: "tester"},
		{Project: "TEST", Title: "Gamma", Prompt: "do 100% of the work_item", Creator: "tester"},
	} {
		_, err := app.nodeSvc.CreateNode(context.Background(), req)
		require.NoError(t, err)
	}
}

func searchIDs(t *testing.T, args ...string) []string {
	t.Helper()
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	var out string
	var err error
	out = captureStdout(t, func() {
		_, err = executeCmd(newSearchCmd(), args...)
	})
	require.NoError(t, err)
	return extractNodeIDs(t, out)
}

// MTIX-146: a positional query filters; it is never silently dropped.
func TestSearchCmd_PositionalQuery_FiltersRows(t *testing.T) {
	initTestApp(t)
	seedSearchCorpus(t)
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"title match", []string{"template"}, []string{"TEST-1"}},
		{"description match, case-insensitive", []string{"GIZMO"}, []string{"TEST-2"}},
		{"prompt match", []string{"work"}, []string{"TEST-3"}},
		{"no match returns nothing", []string{"xyzzynomatch"}, []string{}},
		{"all words must match", []string{"alpha", "gizmo"}, []string{}},
		{"all words match one node", []string{"alpha", "cleanup"}, []string{"TEST-1"}},
		{"percent is literal", []string{"100%"}, []string{"TEST-3"}},
		{"percent alone is not a wildcard", []string{"%"}, []string{"TEST-3"}},
		{"underscore is literal", []string{"work_item"}, []string{"TEST-3"}},
		{"underscore is not a wildcard", []string{"work?item"}, []string{}},
		{"filters combine with the query", []string{"--status", "done", "template"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.want, searchIDs(t, tt.args...))
		})
	}
}

func TestSearchCmd_NoQuery_StillListsByFilters(t *testing.T) {
	initTestApp(t)
	seedSearchCorpus(t)
	assert.Len(t, searchIDs(t), 3)
}

func TestSearchCmd_OverlongQuery_ReturnsInvalidInput(t *testing.T) {
	initTestApp(t)
	seedSearchCorpus(t)
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	_, err := executeCmd(newSearchCmd(), string(long))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid input")
}

// A blank positional argument is a mistake, not "no filter".
func TestSearchCmd_BlankArgument_ReturnsInvalidInput(t *testing.T) {
	initTestApp(t)
	seedSearchCorpus(t)
	for _, blank := range []string{"", "   "} {
		_, err := executeCmd(newSearchCmd(), blank)
		require.Error(t, err, "%q", blank)
		assert.Contains(t, err.Error(), "invalid input")
	}
}
