// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Every stored string is defended at the text boundary; JSON keeps the data.
package sqlite_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func assertWorkflowTextSafe(t *testing.T, text, original string) {
	t.Helper()
	prefix, _, _ := strings.Cut(original, "\n")
	assert.NotContains(t, text, "\x1b")
	assert.NotContains(t, text, "\u202e")
	assert.NotContains(t, text, prefix+"\n", "a stored line must not forge an output line")
}

func TestWorkflowHistory_PeerValuesAndEntryIDAreQuoted(t *testing.T) {
	local, file, _ := workflowHistoryPair(t, 1)
	id, old, next := "entry-probe\n\x1b[31m\u202eend", "old-probe\n\x1b[31m\u202eend", "new-probe\n\x1b[31m\u202eend"
	for i := range file.Nodes[0].History {
		e := &file.Nodes[0].History[i]
		if e.ID == "01U0000000000000000000F000" {
			e.ID, e.Old, e.New = id, &old, &next
		}
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	text := report.String()
	for _, value := range []string{id, old, next} {
		assertWorkflowTextSafe(t, text, value)
	}
	assert.Contains(t, text, `entry-probe\n\x1b[31m\u202eend`)
	rows := workflowHistoryJSON(t, report)["file_history"].([]any)
	require.Len(t, rows, 1)
	row := rows[0].(map[string]any)
	assert.Equal(t, id, row["id"])
	assert.Equal(t, old, row["old"])
	assert.Equal(t, next, row["new"])
}

func TestWorkflowHistory_RendererDefendsTimestampHintAndFieldValues(t *testing.T) {
	// Ingest validates timestamps, but render DTOs are still quoted defensively.
	cases := []struct {
		name string
		set  func(*sqlite.WorkflowConflict, string)
		get  func(*sqlite.WorkflowConflict) string
	}{
		{"timestamp", func(c *sqlite.WorkflowConflict, v string) { c.Fields[0].LocalHistory[0].TS = v }, func(c *sqlite.WorkflowConflict) string { return c.Fields[0].LocalHistory[0].TS }},
		{"hint", func(c *sqlite.WorkflowConflict, v string) { c.Hint = v }, func(c *sqlite.WorkflowConflict) string { return c.Hint }},
		{"local value", func(c *sqlite.WorkflowConflict, v string) { c.Fields[0].Local = v }, func(c *sqlite.WorkflowConflict) string { return c.Fields[0].Local }},
		{"file value", func(c *sqlite.WorkflowConflict, v string) { c.Fields[0].File = v }, func(c *sqlite.WorkflowConflict) string { return c.Fields[0].File }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			local, file, _ := workflowHistoryPair(t, 1)
			report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
			require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
			conflict := &report.WorkflowConflicts[0]
			raw := c.name + "-probe\n\x1b[31m\u202eend"
			c.set(conflict, raw)
			text := report.String()
			assertWorkflowTextSafe(t, text, raw)
			if c.name == "timestamp" {
				assert.Contains(t, text, `timestamp-probe\n\x1b[31m\u202eend`)
			}
			encoded, err := json.Marshal(conflict)
			require.NoError(t, err)
			var decoded sqlite.WorkflowConflict
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, raw, c.get(&decoded), "JSON must preserve the original data")
		})
	}
}

func TestWorkflowHistory_LegacyFileStillReadsTheLocalUID(t *testing.T) {
	local, file, _ := workflowHistoryPair(t, 1)
	file.Nodes[0].UID = ""
	for i := range file.Nodes[0].History {
		file.Nodes[0].History[i].TaskUID = file.Nodes[0].ID
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	report, err := mergeWith(local.s, file, sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	field := workflowHistoryJSON(t, report)
	localRows := field["local_history"].([]any)
	fileRows := field["file_history"].([]any)
	assert.Len(t, localRows, 2, "the local durable uid has its own entries")
	assert.Len(t, fileRows, 2, "the legacy file carries history too")
	ids := []any{}
	for _, row := range localRows {
		ids = append(ids, row.(map[string]any)["id"])
	}
	assert.Contains(t, ids, "01U0000000000000000000L000")
}
