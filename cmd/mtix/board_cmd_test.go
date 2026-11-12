// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func seedBoard(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	mk := func(title string, labels []string, status model.Status) string {
		n, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "TEST", Title: title, Creator: "tester", Labels: labels,
		})
		require.NoError(t, err)
		if status == model.StatusCancelled {
			require.NoError(t, app.nodeSvc.CancelNode(ctx, n.ID, "test", "tester", false))
		}
		return n.ID
	}
	mk("Now alpha", []string{"desk:now"}, model.StatusOpen)
	mk("Backlog beta", []string{"desk:backlog"}, model.StatusOpen)
	mk("Plain gamma", nil, model.StatusOpen)
	mk("Dropped delta", []string{"desk:now"}, model.StatusCancelled)
}

type boardJSON struct {
	Columns []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	} `json:"columns"`
}

func runBoard(t *testing.T, jsonMode bool, args ...string) string {
	t.Helper()
	app.jsonOutput = jsonMode
	cmd := newBoardCmd()
	cmd.SetArgs(args)
	var execErr error
	out := captureStdout(t, func() { execErr = cmd.Execute() })
	require.NoError(t, execErr)
	return out
}

func boardCounts(t *testing.T, args ...string) map[string]int {
	t.Helper()
	var b boardJSON
	require.NoError(t, json.Unmarshal([]byte(runBoard(t, true, args...)), &b))
	m := map[string]int{}
	for _, c := range b.Columns {
		m[c.Name] = c.Count
	}
	return m
}

// MTIX-255.1: board groups by status with counts; filters narrow it.
func TestBoardCmd_GroupsCountsAndFilters(t *testing.T) {
	initTestApp(t)
	seedBoard(t)
	c := boardCounts(t)
	assert.Equal(t, 3, c["open"])
	assert.Equal(t, 1, c["cancelled"])
	assert.Equal(t, 0, c["done"])
	assert.Len(t, c, 7)

	c = boardCounts(t, "--labels", "desk:now")
	assert.Equal(t, 1, c["open"])
	assert.Equal(t, 1, c["cancelled"])
	c = boardCounts(t, "--labels", "desk:now,desk:backlog")
	assert.Equal(t, 2, c["open"])
	c = boardCounts(t, "--labels", "missing")
	assert.Equal(t, 0, c["open"])
	c = boardCounts(t, "--assignee", "nobody")
	assert.Equal(t, 0, c["open"])
}

func TestBoardCmd_Under_RestrictsToSubtree(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	root, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "root", Creator: "t"})
	require.NoError(t, err)
	_, err = app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "child", Creator: "t", ParentID: root.ID})
	require.NoError(t, err)
	_, err = app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "other", Creator: "t"})
	require.NoError(t, err)
	assert.Equal(t, 3, boardCounts(t)["open"])
	// --under includes the parent itself plus descendants, like list.
	assert.Equal(t, 2, boardCounts(t, "--under", root.ID)["open"])
}

// MTIX-255.1: --limit caps items per column but never the count.
func TestBoardCmd_Limit_CapsItemsNotCounts(t *testing.T) {
	initTestApp(t)
	seedBoard(t)
	var b boardJSON
	require.NoError(t, json.Unmarshal([]byte(runBoard(t, true, "--limit", "1")), &b))
	assert.Equal(t, 3, b.Columns[0].Count)
	assert.Len(t, b.Columns[0].Items, 1)
	app.jsonOutput = false
	cmd := newBoardCmd()
	cmd.SetArgs([]string{"--limit", "0"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	assert.ErrorIs(t, cmd.Execute(), model.ErrInvalidInput)
}

// MTIX-255.1: the JSON key set is pinned.
func TestBoardCmd_JSON_KeysArePinned(t *testing.T) {
	initTestApp(t)
	seedBoard(t)
	var raw struct {
		Columns []map[string]json.RawMessage `json:"columns"`
	}
	require.NoError(t, json.Unmarshal([]byte(runBoard(t, true)), &raw))
	require.Len(t, raw.Columns, 7)
	for _, col := range raw.Columns {
		keys := []string{}
		for k := range col {
			keys = append(keys, k)
		}
		assert.ElementsMatch(t, []string{"name", "count", "items"}, keys)
	}
	var items []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw.Columns[0]["items"], &items))
	require.NotEmpty(t, items)
	keys := []string{}
	for k := range items[0] {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"id", "title", "priority", "assignee"}, keys)
	var empty []json.RawMessage
	require.NoError(t, json.Unmarshal(raw.Columns[3]["items"], &empty))
	assert.NotNil(t, empty)
}

func TestBoardCmd_EmptyBoard_PrintsAllColumns(t *testing.T) {
	initTestApp(t)
	out := runBoard(t, false)
	for _, n := range []string{"open (0)", "in_progress (0)", "blocked (0)", "deferred (0)", "done (0)", "cancelled (0)", "invalidated (0)"} {
		assert.Contains(t, out, n)
	}
}

// MTIX-255.1: table shows counts, and "... N more" when truncated.
func TestBoardCmd_Table_ShowsCountsAndTruncation(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	initTestApp(t)
	seedBoard(t)
	out := runBoard(t, false, "--limit", "2")
	assert.Contains(t, out, "open (3)")
	assert.Contains(t, out, "Now alpha")
	assert.Contains(t, out, "... 1 more")
	assert.Contains(t, out, "cancelled (1)")
}

// MTIX-255.1: non-UTF-8 consoles get plain ASCII.
func TestBoardCmd_Table_NonUTF8ConsoleIsASCII(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	initTestApp(t)
	seedBoard(t)
	out := runBoard(t, false)
	for _, r := range out {
		require.Less(t, int(r), 128, "non-ASCII rune %q in %q", r, out)
	}
	assert.Contains(t, out, "[ ] ")
	assert.True(t, strings.Contains(out, "open (3)"))
}

func TestBoardCmd_IsReadOnly_NoNodeChanges(t *testing.T) {
	initTestApp(t)
	seedBoard(t)
	before, _, err := app.nodeSvc.ListNodes(context.Background(), service.NodeFilter{}, service.ListOptions{Limit: 100})
	require.NoError(t, err)
	_ = runBoard(t, true)
	_ = runBoard(t, false)
	after, _, err := app.nodeSvc.ListNodes(context.Background(), service.NodeFilter{}, service.ListOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, after, len(before))
	for i := range before {
		assert.Equal(t, before[i].ContentHash, after[i].ContentHash)
		assert.Equal(t, before[i].UpdatedAt, after[i].UpdatedAt)
	}
}

// MTIX-255.1: a non-ASCII title never reaches a non-UTF-8 console as raw bytes.
func TestBoardCmd_Table_NonUTF8ConsoleReplacesNonASCIITitles(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	initTestApp(t)
	_, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project: "TEST", Title: "Café plan", Creator: "tester",
	})
	require.NoError(t, err)
	out := runBoard(t, false)
	assert.Contains(t, out, "Caf? plan")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	assert.Contains(t, runBoard(t, false), "Café plan")
}

// MTIX-255.1: the documented defaults are part of the contract.
func TestBoardCmd_Flags_DefaultLimitIsTwenty(t *testing.T) {
	f := newBoardCmd().Flags().Lookup("limit")
	require.NotNil(t, f)
	assert.Equal(t, "20", f.DefValue)
	for _, name := range []string{"under", "labels", "assignee", "project", "all-projects"} {
		assert.NotNil(t, newBoardCmd().Flags().Lookup(name), name)
	}
}

func TestConsoleIsUTF8_ReadsLocaleVariablesInPrecedence(t *testing.T) {
	tests := []struct {
		name          string
		all, ctype, l string
		want          bool
	}{
		{"LC_ALL wins over LANG", "C", "", "en_US.UTF-8", false},
		{"LC_ALL utf8", "en_US.utf8", "", "C", true},
		{"LC_CTYPE before LANG", "", "C", "en_US.UTF-8", false},
		{"LANG only", "", "", "en_US.UTF-8", true},
		{"none set", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LC_ALL", tt.all)
			t.Setenv("LC_CTYPE", tt.ctype)
			t.Setenv("LANG", tt.l)
			assert.Equal(t, tt.want, consoleIsUTF8())
		})
	}
}
