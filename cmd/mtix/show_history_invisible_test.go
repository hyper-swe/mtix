// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
)

// show --history applies the shared rule to every history field it prints:
// a label that holds a bidirectional override, and an imported actor, field or
// value that hold invisible format characters or line separators, never reach
// the terminal, while --json keeps the exact text (MTIX-100.7).
func TestRunShow_HistoryText_RemovesInvisibleFormatCharacters(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	labels := []string{"x\u202eSSAP\u202cy"}
	require.NoError(t, app.nodeSvc.UpdateNode(context.Background(), id, &store.NodeUpdate{Labels: labels}))
	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	_, err = app.store.WriteDB().Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value)
		VALUES ('01U0000000000000000000A002', ?, '2999-01-01T00:00:00.000000000Z', ?, 'import', 'change', ?, ?, ?)`,
		node.UID, "act\u202eor\u200b", "fi\u2066eld", "\"v\u200dx\"", "\"a\u2028b\"")
	require.NoError(t, err)

	app.jsonOutput = false
	out := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{history: true, historyLimit: 10})) })
	for _, r := range []string{"\u202e", "\u202c", "\u200b", "\u2066", "\u200d", "\u2028"} {
		assert.NotContains(t, out, r, "invisible character %U must not reach the terminal", []rune(r)[0])
	}
	assert.Contains(t, out, "actor")
	assert.Contains(t, out, "xSSAPy", "the label text stays, without the override")

	app.jsonOutput = true
	js := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{historyLimit: 10})) })
	assert.Contains(t, js, "x\u202eSSAP\u202cy", "JSON keeps the exact text")
}

// A line or paragraph separator in an imported actor, field or value must not
// start a new line in show --history: like singleLine, the history text shows
// it as a space, so every entry stays on one line (MTIX-100.7).
func TestRunShow_HistoryText_LineSeparatorsStayOnOneLineAsSpaces(t *testing.T) {
	initTestApp(t)
	id := createShowNode(t, "d")
	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	_, err = app.store.WriteDB().Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value)
		VALUES ('01U0000000000000000000A003', ?, '2999-01-01T00:00:00.000000000Z', ?, 'import', 'change', ?, ?, ?)`,
		node.UID, "act\u2028or", "fi\u2029eld", "\"v\u2028x\"", "\"a\u2029b\"")
	require.NoError(t, err)

	app.jsonOutput = false
	out := captureStdout(t, func() { require.NoError(t, runShowWith(id, showOptions{history: true, historyLimit: 10})) })

	assert.Contains(t, out, "fi eld  act or (import)", "separators in history text show as spaces")
	assert.Contains(t, out, "\"v x\" -> \"a b\"")
	history := out[strings.Index(out, "History:"):]
	for _, line := range strings.Split(strings.TrimRight(history, "\n"), "\n")[1:] {
		assert.True(t, strings.HasPrefix(line, "  2"), "every history entry stays on one line, got %q", line)
	}
}
