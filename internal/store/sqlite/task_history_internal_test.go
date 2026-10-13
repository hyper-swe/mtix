// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHistorySnapshotSQL_UsesPrimaryKeyIndex pins the plan of the constant
// snapshot statements the recorder really runs (Rule 1b): a full scan of
// nodes on every write breaks NFR-1.1 on a large board (F2).
func TestHistorySnapshotSQL_UsesPrimaryKeyIndex(t *testing.T) {
	s := newInternalTestStore(t)
	tests := []struct {
		name  string
		query string
		args  []any
	}{
		{"single task", historySnapshotSQL, []any{"PROJ-1"}},
		{"subtree range", historySubtreeSQL, []any{"PROJ-1.", "PROJ-1/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A test may prefix EXPLAIN QUERY PLAN to the package's own constant SQL.
			rows, err := s.readDB.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+tt.query, tt.args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
				plan = append(plan, detail)
			}
			require.NoError(t, rows.Err())
			joined := strings.Join(plan, "; ")
			assert.Contains(t, joined, "SEARCH nodes USING")
			assert.NotContains(t, joined, "SCAN nodes")
		})
	}
}

// TestHistoryFields_EveryNodesColumnIsRecordedOrExcluded fails when a nodes
// column is neither a history field nor on the documented exclusion list, so a
// new column cannot be forgotten (F7); it also checks the snapshot SQL column
// order matches historyFieldNames (F5).
func TestHistoryFields_EveryNodesColumnIsRecordedOrExcluded(t *testing.T) {
	s := newInternalTestStore(t)
	rows, err := s.readDB.QueryContext(context.Background(), `SELECT name FROM pragma_table_info('nodes')`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	recorded := map[string]bool{}
	for _, f := range historyFieldNames() {
		recorded[f] = true
	}
	recorded["parent_id"] = true // field "parent"
	excluded := historyExcludedColumns()
	for rows.Next() {
		var col string
		require.NoError(t, rows.Scan(&col))
		_, isExcluded := excluded[col]
		assert.True(t, recorded[col] || isExcluded, "nodes column %q is neither recorded nor excluded", col)
	}
	require.NoError(t, rows.Err())

	cols := historySnapshotSQL[strings.Index(historySnapshotSQL, "COALESCE(uid, ''),")+len("COALESCE(uid, ''),") : strings.Index(historySnapshotSQL, "FROM nodes")]
	var selected []string
	for _, c := range strings.Split(cols, ",") {
		if c = strings.TrimSpace(c); c != "" {
			selected = append(selected, c)
		}
	}
	want := historyFieldNames()
	require.Len(t, selected, len(want))
	for i, c := range selected {
		if want[i] == "parent" {
			assert.Equal(t, "parent_id", c)
		} else {
			assert.Equal(t, want[i], c)
		}
	}
	assert.Equal(t, strings.TrimSpace(historySnapshotSQL[:strings.Index(historySnapshotSQL, "FROM nodes")]),
		strings.TrimSpace(historySubtreeSQL[:strings.Index(historySubtreeSQL, "FROM nodes")]), "both statements read the same columns")
}
