// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for the helpers recover uses to restore an unreadable node JSON
// column from the mirror (MTIX-95.31.3): finding every unreadable column
// in a scan error, copying a known column, and never claiming a restore of
// a column it does not know.
package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestUnreadableColumns_ErrorShapes_FindsEveryColumn verifies that every
// unreadable column is found in an error, alone, joined (as scanExportNode
// returns them) or wrapped, and that nothing is found in any other error.
func TestUnreadableColumns_ErrorShapes_FindsEveryColumn(t *testing.T) {
	a := &unreadableColumnError{nodeID: "RC-1", column: columnAnnotations, cause: sql.ErrNoRows}
	b := &unreadableColumnError{nodeID: "RC-1", column: columnActivity, cause: sql.ErrNoRows}
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"nil", nil, nil},
		{"other error", sql.ErrNoRows, nil},
		{"the sentinel alone", fmt.Errorf("x: %w", errUnreadableNodeColumn), nil},
		{"one column", a, []string{columnAnnotations}},
		{"joined columns", errors.Join(a, b), []string{columnAnnotations, columnActivity}},
		{"wrapped join", fmt.Errorf("salvage: %w", errors.Join(a, b)), []string{columnAnnotations, columnActivity}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, c := range unreadableColumns(tt.err) {
				got = append(got, c.column)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestUnreadableColumnError_Message_KeepsFormatAndChain verifies the error
// reads as the MTIX-95.31.1 message did and still matches the sentinel and
// the parse error.
func TestUnreadableColumnError_Message_KeepsFormatAndChain(t *testing.T) {
	err := &unreadableColumnError{nodeID: "RC-1", column: columnAnnotations, cause: sql.ErrNoRows}
	assert.Equal(t, "node RC-1 column annotations: unreadable node column: "+sql.ErrNoRows.Error(), err.Error())
	assert.ErrorIs(t, err, errUnreadableNodeColumn)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// TestScanExportNode_TwoUnreadableColumns_NamesBoth verifies that the scan
// error of a row with two unparsable JSON columns names both columns.
func TestScanExportNode_TwoUnreadableColumns_NamesBoth(t *testing.T) {
	j := exportNodeJSON{
		annotations: sql.NullString{String: "{torn", Valid: true},
		commitRefs:  sql.NullString{String: "[1,", Valid: true},
	}
	n := exportNode{ID: "RC-1"}
	var got []string
	for _, c := range unreadableColumns(j.decodeInto(&n)) {
		assert.Equal(t, "RC-1", c.nodeID)
		got = append(got, c.column)
	}
	assert.Equal(t, []string{columnCommitRefs, columnAnnotations}, got)
}

// TestCopyNodeColumn_EachColumn_CopiesItOnly verifies each known column is
// copied with its entry count, the other columns are left alone, and an
// unknown column is reported as not copied.
func TestCopyNodeColumn_EachColumn_CopiesItOnly(t *testing.T) {
	src := exportNode{
		Annotations: []model.Annotation{{ID: "a1"}, {ID: "a2"}},
		Activity:    []model.ActivityEntry{{ID: "e1"}},
		CodeRefs:    []model.CodeRef{{File: "f.go"}, {File: "g.go"}, {File: "h.go"}},
		CommitRefs:  []string{"abc"},
		Metadata:    `{"k":"v"}`,
	}
	tests := []struct {
		column      string
		wantEntries int
		wantOK      bool
		check       func(t *testing.T, dst exportNode)
	}{
		{columnAnnotations, 2, true, func(t *testing.T, dst exportNode) {
			assert.Equal(t, src.Annotations, dst.Annotations)
			assert.Nil(t, dst.Activity)
		}},
		{columnActivity, 1, true, func(t *testing.T, dst exportNode) {
			assert.Equal(t, src.Activity, dst.Activity)
			assert.Nil(t, dst.Annotations)
		}},
		{columnCodeRefs, 3, true, func(t *testing.T, dst exportNode) {
			assert.Equal(t, src.CodeRefs, dst.CodeRefs)
			assert.Nil(t, dst.CommitRefs)
		}},
		{columnCommitRefs, 1, true, func(t *testing.T, dst exportNode) {
			assert.Equal(t, src.CommitRefs, dst.CommitRefs)
			assert.Nil(t, dst.CodeRefs)
		}},
		{"metadata", 0, false, func(t *testing.T, dst exportNode) {
			assert.Equal(t, exportNode{}, dst)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.column, func(t *testing.T) {
			var dst exportNode
			entries, ok := copyNodeColumn(&dst, &src, tt.column)
			assert.Equal(t, tt.wantEntries, entries)
			assert.Equal(t, tt.wantOK, ok)
			tt.check(t, dst)
		})
	}
}

// TestRestoreColumnsFromMirror_UnknownColumn_NotedAsDropped verifies that a
// column recover cannot copy is noted as dropped, never as restored, even
// when the mirror holds a usable copy of the node.
func TestRestoreColumnsFromMirror_UnknownColumn_NotedAsDropped(t *testing.T) {
	node := exportNode{ID: "RC-1", CreatedAt: "2026-09-01T10:00:00Z", UpdatedAt: "2026-09-01T10:00:00Z"}
	mirror := &mirrorSource{
		path:     "/p/.mtix/tasks.json",
		data:     &ExportData{SchemaVersion: SchemaVersionV1, Nodes: []exportNode{node}},
		verified: true,
		byID:     map[string][]int{"RC-1": {0}},
	}
	nodes := map[string]exportNode{"RC-1": node}
	col := &unreadableColumnError{nodeID: "RC-1", column: "metadata", cause: sql.ErrNoRows}
	res := &RecoverResult{}

	restoreColumnsFromMirror(nodes, []salvagedColumn{{key: "RC-1", col: col}}, mirror, res)

	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "dropped")
	assert.Contains(t, res.Notes[0], "recover cannot restore this column")
	assert.NotContains(t, res.Notes[0], "restored")
}

// TestRestoreColumnsFromMirror_KeyDiffersFromRowID_SkippedWithNote covers a
// damaged primary-key index (MTIX-95.31.3): salvage read, under the key
// RC-1, a row that carries the id RC-2, and that row's annotations do not
// parse. Recover must not take the column from the mirror: neither the node
// kept under RC-1 nor the readable RC-2 changes, and one note says why the
// column is dropped.
func TestRestoreColumnsFromMirror_KeyDiffersFromRowID_SkippedWithNote(t *testing.T) {
	const at = "2026-09-01T10:00:00Z"
	comment := func(id, text string) []model.Annotation {
		return []model.Annotation{{ID: id, Text: text, CreatedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)}}
	}
	misread := exportNode{ID: "RC-2", CreatedAt: at, UpdatedAt: at}
	readable := exportNode{ID: "RC-2", CreatedAt: at, UpdatedAt: at, Annotations: comment("local", "local comment")}
	mirror := &mirrorSource{
		path: "/p/.mtix/tasks.json",
		data: &ExportData{SchemaVersion: SchemaVersionV1, Nodes: []exportNode{
			{ID: "RC-1", CreatedAt: at, UpdatedAt: at, Annotations: comment("m1", "mirror copy of RC-1")},
			{ID: "RC-2", CreatedAt: at, UpdatedAt: at, Annotations: comment("m2", "mirror copy of RC-2")},
		}},
		verified: true,
		byID:     map[string][]int{"RC-1": {0}, "RC-2": {1}},
	}
	nodes := map[string]exportNode{"RC-1": misread, "RC-2": readable}
	col := &unreadableColumnError{nodeID: "RC-2", column: columnAnnotations, cause: sql.ErrNoRows}
	res := &RecoverResult{}

	restoreColumnsFromMirror(nodes, []salvagedColumn{{key: "RC-1", col: col}}, mirror, res)

	assert.Len(t, nodes, 2)
	assert.Equal(t, misread, nodes["RC-1"], "the node kept under RC-1 gets no column from the mirror")
	assert.Equal(t, readable, nodes["RC-2"], "the readable RC-2 keeps its own comments")
	require.Len(t, res.Notes, 1, "notes: %v", res.Notes)
	assert.Contains(t, res.Notes[0], "node RC-2 column annotations:")
	assert.Contains(t, res.Notes[0], "dropped")
	assert.Contains(t, res.Notes[0], "the database row read under id RC-1 carries id RC-2")
	assert.NotContains(t, res.Notes[0], "restored")
}

// TestEntryCount_Plural verifies the entry count wording of the notes.
func TestEntryCount_Plural(t *testing.T) {
	assert.Equal(t, "0 entries", entryCount(0))
	assert.Equal(t, "1 entry", entryCount(1))
	assert.Equal(t, "2 entries", entryCount(2))
}
