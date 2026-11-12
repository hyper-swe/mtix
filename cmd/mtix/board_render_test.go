// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// MTIX-255.1: every column has a pinned ASCII marker on a non-UTF-8 console.
func TestBoardMarker_NonUTF8_EveryColumnHasItsASCIIMarker(t *testing.T) {
	want := map[string]string{
		"open": "[ ]", "in_progress": "[~]", "blocked": "[!]", "deferred": "[-]",
		"done": "[x]", "cancelled": "[/]", "invalidated": "[?]",
	}
	require.Len(t, want, len(model.AllStatuses()))
	for _, st := range model.AllStatuses() {
		t.Run(string(st), func(t *testing.T) {
			assert.Equal(t, want[string(st)], boardMarker(string(st), false))
		})
	}
}

// MTIX-255.1: the rendered table uses each column's marker on its own lines.
func TestRenderBoardTable_NonUTF8_EachColumnLineCarriesItsMarker(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	board := &service.Board{}
	markers := map[string]string{
		"open": "[ ]", "in_progress": "[~]", "blocked": "[!]", "deferred": "[-]",
		"done": "[x]", "cancelled": "[/]", "invalidated": "[?]",
	}
	for _, st := range model.AllStatuses() {
		board.Columns = append(board.Columns, service.BoardColumn{
			Name: string(st), Count: 1,
			Items: []service.BoardItem{{ID: "T-" + string(st), Title: "card", Priority: 2}},
		})
	}
	out := captureStdout(t, func() { require.NoError(t, renderBoardTable(board, false)) })
	for name, m := range markers {
		assert.Contains(t, out, "  "+m+" T-"+name+"  P2  card", name)
	}
}

// MTIX-255.1: control characters and newlines in a title never reach the table.
func TestBoardCmd_Table_TitleControlCharactersAreSanitised(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	initTestApp(t)
	_, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project: "TEST", Title: "evil\x1b[31m red\nFAKE (9)\ttab\rcr", Creator: "tester",
	})
	require.NoError(t, err)
	out := runBoard(t, false)
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\r")
	assert.NotContains(t, out, "\t")
	for _, line := range strings.Split(out, "\n") {
		assert.False(t, strings.HasPrefix(line, "FAKE"), "title line injected: %q", line)
	}
	assert.Contains(t, out, "evil[31m red FAKE (9) tab")
}

// MTIX-255.1: --json is byte-exact against a golden file (key order and shape).
func TestBoardCmd_JSON_MatchesGoldenFile(t *testing.T) {
	golden, err := filepath.Abs(filepath.Join("testdata", "board", "board.golden.json"))
	require.NoError(t, err)
	initTestApp(t)
	seedBoard(t)
	got := runBoard(t, true)
	if os.Getenv("MTIX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(golden, []byte(got), 0o600))
	}
	want, err := os.ReadFile(golden) //nolint:gosec // fixed test path
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
}

// MTIX-255.1: --project and --all-projects scope the board; soft-deleted
// nodes are excluded from counts and items.
func TestBoardCmd_ProjectScopeAndSoftDelete(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	mk := func(project, title string) string {
		n, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: project, Title: title, Creator: "tester",
		})
		require.NoError(t, err)
		return n.ID
	}
	mk("TEST", "home one")
	mk("TEST", "home two")
	mk("OTHER", "away one")
	gone := mk("TEST", "home deleted")
	require.NoError(t, app.nodeSvc.DeleteNode(ctx, gone, false, "tester"))

	assert.Equal(t, 2, boardCounts(t)["open"], "default scope is the primary project")
	assert.Equal(t, 1, boardCounts(t, "--project", "OTHER")["open"])
	assert.Equal(t, 3, boardCounts(t, "--all-projects")["open"])
	assert.NotContains(t, runBoard(t, true, "--all-projects"), "home deleted")

	cmd := newBoardCmd()
	cmd.SetArgs([]string{"--project", "OTHER", "--all-projects"})
	var execErr error
	_ = captureStdout(t, func() { execErr = cmd.Execute() })
	assert.ErrorIs(t, execErr, model.ErrInvalidInput)
}
