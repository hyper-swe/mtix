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
	"github.com/hyper-swe/mtix/internal/store"
)

func changePriorityTimes(t *testing.T, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		p := model.Priority(i%4 + 2)
		require.NoError(t, app.nodeSvc.UpdateNode(context.Background(), id, &store.NodeUpdate{Priority: &p}))
	}
}

func TestRunShow_JSON_ListsHistoryNewestFirstWithItsFields(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 3)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{historyLimit: 50})) })
	var got struct {
		History []struct {
			Field, Origin, Actor string
			Old, New             *string
			Timestamp            string
		} `json:"history"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.GreaterOrEqual(t, len(got.History), 3)
	assert.Equal(t, "priority", got.History[0].Field)
	assert.NotEmpty(t, got.History[0].Actor)
	assert.NotEmpty(t, got.History[0].Origin)
	assert.NotNil(t, got.History[0].New)
	assert.True(t, got.History[0].Timestamp >= got.History[1].Timestamp, "newest first")
}

func TestRunShow_HistoryFlag_PrintsEntriesAndRespectsHistoryLimit(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 4)
	app.jsonOutput = false
	plain := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{})) })
	assert.NotContains(t, plain, "History:")
	full := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{history: true, historyLimit: 50})) })
	assert.Contains(t, full, "History:")
	assert.Contains(t, full, "priority")
	limited := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{history: true, historyLimit: 2})) })
	assert.Equal(t, 2, strings.Count(limited, "  priority"), limited)
}

func TestShowCmd_HistoryFlags_AreDeclaredWithDefault50(t *testing.T) {
	cmd := newShowCmd()
	f := cmd.Flags().Lookup("history-limit")
	require.NotNil(t, f)
	assert.Equal(t, "50", f.DefValue)
	require.NotNil(t, cmd.Flags().Lookup("history"))
	assert.Contains(t, showLongHelp, "History")
}

// Imported history text reaches the terminal only after control characters are
// removed; --json keeps the exact text.
func TestRunShow_HistoryText_StripsTerminalControlSequences(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	_, err = app.store.WriteDB().Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value)
		VALUES ('01U0000000000000000000A001', ?, '2999-01-01T00:00:00.000000000Z', ?, 'import', 'change', 'status', ?, '"done"')`,
		node.UID, "evil\x1b[31mactor\x9b", "\"x\x1b]0;title\x07\"")
	require.NoError(t, err)
	app.jsonOutput = false
	out := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{history: true, historyLimit: 5})) })
	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x9b")
	assert.NotContains(t, out, "\x07")
	assert.Contains(t, out, "evil[31mactor")
	app.jsonOutput = true
	js := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{historyLimit: 5})) })
	assert.Contains(t, js, `\u001b[31mactor`, "JSON keeps the exact text, escaped")
}

// show --json keeps the node record's key order and names the entry time ts,
// as the export does.
func TestRunShow_JSON_KeepsNodeKeyOrderAndNamesTheTimeTs(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	changePriorityTimes(t, id, 1)
	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	plain, err := json.MarshalIndent(node, "", "  ")
	require.NoError(t, err)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{historyLimit: 5})) })
	body := strings.TrimSuffix(strings.TrimSpace(out), "}")
	head := strings.TrimSuffix(strings.TrimSuffix(string(plain), "}"), "\n")
	assert.True(t, strings.HasPrefix(body, head), "the node record keeps its bytes and order\n%s", out)
	assert.Contains(t, out, `"ts":`)
	assert.NotContains(t, out, `"timestamp"`)
}

// A task with no history still carries the history key, as an empty list, so a
// reader can use it without checking for the key first.
func TestRunShow_JSON_TaskWithoutHistory_EmitsAnEmptyHistoryList(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "e")
	_, err := app.store.WriteDB().Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{historyLimit: 50})) })
	var got map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.JSONEq(t, `[]`, string(got["history"]), out)
}
