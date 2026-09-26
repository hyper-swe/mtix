// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.3 in mtix recover (FR-26.5): a node JSON column the
// database cannot read is taken from the .mtix/tasks.json mirror's copy of
// the same node when that copy is usable, and the recovery note names the
// mirror; otherwise the node is salvaged without the column and the note
// says the column was dropped and why. Written red-first.
package sqlite

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// The reviewer's scenario: a comment on RC-1 (MTIX-95.31.3).
const (
	rcCommentID   = "01J9RCCOMMENT0000000000001"
	rcCommentText = "review PASS: the bar is green, evidence attached"
	rcOtherUID    = "01J9RCOTHERTASK00000000001"
	// tornJSON is a JSON cell cut short, as a torn page leaves it.
	tornJSON = `[{"id":"01J9RC`
)

// corruptColumnSQL overwrites one JSON column of one node with the value
// bound to the first parameter (MTIX-95.31.3). Identifiers cannot be bound,
// so each column has its own constant statement.
func corruptColumnSQL(t *testing.T, column string) string {
	t.Helper()
	switch column {
	case "annotations":
		return `UPDATE nodes SET annotations = ? WHERE id = ?`
	case "activity":
		return `UPDATE nodes SET activity = ? WHERE id = ?`
	case "code_refs":
		return `UPDATE nodes SET code_refs = ? WHERE id = ?`
	case "commit_refs":
		return `UPDATE nodes SET commit_refs = ? WHERE id = ?`
	}
	t.Fatalf("no corruption statement for column %q", column)
	return ""
}

// seedMirrorColumnFixture creates a store holding RC-1, with a comment (the
// write mtix comment makes) and a value in every other JSON column, and an
// unaffected RC-2. It returns the store and the database path; the caller
// exports the mirror, then corrupts cells and closes the store.
func seedMirrorColumnFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for i, title := range []string{"reviewed task with a comment", "unaffected neighbour task"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("RC-%d", i+1), Project: "RC", Depth: 0, Seq: i + 1, Title: title,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: at, UpdatedAt: at,
		}))
	}
	require.NoError(t, s.SetAnnotations(ctx, "RC-1", []model.Annotation{{
		ID: rcCommentID, Author: "reviewer", Text: rcCommentText, CreatedAt: at,
	}}))
	_, err = s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET activity = ?, code_refs = ?, commit_refs = ? WHERE id = ?`,
		`[{"id":"01J9RCACTIVITY000000000001","type":"comment","author":"reviewer","text":"activity kept","created_at":"2026-09-01T10:00:00Z"}]`,
		`[{"file":"internal/store/sqlite/recover.go","line":51,"function":"Recover"}]`,
		`["dd15992"]`, "RC-1")
	require.NoError(t, err)
	return s, dbPath
}

// exportMirrorOf exports s as a tasks.json mirror, applies edit to it
// (nil: none) and writes it to a temp file. The checksum is recomputed
// after the edit unless staleChecksum is set.
func exportMirrorOf(t *testing.T, s *Store, edit func(t *testing.T, m *ExportData), staleChecksum bool) (string, *ExportData) {
	t.Helper()
	m, err := s.Export(context.Background(), "RC", "test-version")
	require.NoError(t, err)
	if edit != nil {
		edit(t, m)
		m.NodeCount = len(m.Nodes)
	}
	if !staleChecksum {
		require.NoError(t, RecomputeExportChecksum(m))
	}
	return writeMirror(t, t.TempDir(), m), m
}

// corruptAndClose overwrites each column of RC-1 with torn JSON, then
// closes the store so recover reads the file as a crash would leave it.
func corruptAndClose(t *testing.T, s *Store, columns ...string) {
	t.Helper()
	for _, column := range columns {
		_, err := s.WriteDB().ExecContext(context.Background(), corruptColumnSQL(t, column), tornJSON, "RC-1")
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
}

// mirrorNodeOf returns the node id of m, failing the test when absent.
func mirrorNodeOf(t *testing.T, m *ExportData, id string) *exportNode {
	t.Helper()
	for i := range m.Nodes {
		if m.Nodes[i].ID == id {
			return &m.Nodes[i]
		}
	}
	t.Fatalf("mirror holds no node %s", id)
	return nil
}

// recoveredNode returns the node id of the recovered export.
func recoveredNode(t *testing.T, res *RecoverResult, id string) exportNode {
	t.Helper()
	for _, n := range res.Export.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("recovered export holds no node %s", id)
	return exportNode{}
}

// columnNote returns the recovery note about column of node id, or "".
func columnNote(res *RecoverResult, id, column string) string {
	prefix := "node " + id + " column " + column + ":"
	for _, n := range res.Notes {
		if strings.HasPrefix(n, prefix) {
			return n
		}
	}
	return ""
}

// TestRecover_CommentCellCorrupted_RestoresCommentFromMirror is the
// reviewer's scenario (MTIX-95.31.3): a comment on RC-1, its annotations
// cell corrupted; recover restores the comment from .mtix/tasks.json, names
// the mirror in its note, and the result imports with the comment.
func TestRecover_CommentCellCorrupted_RestoresCommentFromMirror(t *testing.T) {
	s, dbPath := seedMirrorColumnFixture(t)
	mirrorPath, _ := exportMirrorOf(t, s, nil, false)
	corruptAndClose(t, s, "annotations")

	res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)

	assert.Contains(t, res.RecoveredIDs, "RC-1", "RC-1 is salvaged from the database")
	assert.NotContains(t, res.FromMirror, "RC-1", "only its unreadable column comes from the mirror")
	rc1 := recoveredNode(t, res, "RC-1")
	require.Len(t, rc1.Annotations, 1, "the comment is restored from the mirror")
	assert.Equal(t, rcCommentID, rc1.Annotations[0].ID)
	assert.Equal(t, rcCommentText, rc1.Annotations[0].Text)
	assert.Equal(t, "reviewer", rc1.Annotations[0].Author)

	note := columnNote(res, "RC-1", "annotations")
	assert.Contains(t, note, "restored from the mirror", "notes: %v", res.Notes)
	assert.Contains(t, note, mirrorPath, "the note names the mirror file")
	assert.Contains(t, note, "1 entry")
	assert.NotContains(t, note, "dropped")

	fresh := importRoundTrip(t, res.Export)
	got, err := fresh.GetNode(context.Background(), "RC-1")
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1, "the imported store holds the comment")
	assert.Equal(t, rcCommentText, got.Annotations[0].Text)
}

// TestRecover_UnreadableColumns_RestoredFromMirror covers every JSON column
// recover decodes, alone and together, and the usable copies whose file or
// identity differ from the plain case (MTIX-95.31.3): each unreadable column
// equals the mirror copy's, its note names the mirror, and the readable
// columns keep the database's values.
func TestRecover_UnreadableColumns_RestoredFromMirror(t *testing.T) {
	tests := []struct {
		name      string
		columns   []string
		edit      func(t *testing.T, m *ExportData)
		stale     bool
		wantNotes []string
	}{
		{name: "activity", columns: []string{"activity"}, wantNotes: []string{"1 entry"}},
		{name: "code_refs", columns: []string{"code_refs"}, wantNotes: []string{"1 entry"}},
		{name: "commit_refs", columns: []string{"commit_refs"}, wantNotes: []string{"1 entry"}},
		{name: "annotations and activity together", columns: []string{"annotations", "activity"}},
		{name: "mirror copy without a uid (older board)", columns: []string{"annotations"},
			edit: func(t *testing.T, m *ExportData) { mirrorNodeOf(t, m, "RC-1").UID = "" }},
		{name: "mirror copy holds two comments", columns: []string{"annotations"},
			edit: func(t *testing.T, m *ExportData) {
				n := mirrorNodeOf(t, m, "RC-1")
				second := n.Annotations[0]
				second.ID, second.Text = "01J9RCCOMMENT0000000000002", "second comment"
				n.Annotations = append(n.Annotations, second)
			},
			wantNotes: []string{"2 entries"}},
		{name: "mirror copy holds no comment", columns: []string{"annotations"},
			edit:      func(t *testing.T, m *ExportData) { mirrorNodeOf(t, m, "RC-1").Annotations = nil },
			wantNotes: []string{"0 entries"}},
		{name: "mirror checksum stale", columns: []string{"annotations"}, stale: true,
			edit:      func(t *testing.T, m *ExportData) { mirrorNodeOf(t, m, "RC-2").Title = "edited by hand" },
			wantNotes: []string{"mirror checksum did not verify"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, dbPath := seedMirrorColumnFixture(t)
			mirrorPath, mirror := exportMirrorOf(t, s, tt.edit, tt.stale)
			corruptAndClose(t, s, tt.columns...)

			res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)

			rc1 := recoveredNode(t, res, "RC-1")
			want := *mirrorNodeOf(t, mirror, "RC-1")
			for _, column := range []string{"annotations", "activity", "code_refs", "commit_refs"} {
				got, marshalErr := jsonField(rc1, column)
				require.NoError(t, marshalErr)
				wantJSON, marshalErr := jsonField(want, column)
				require.NoError(t, marshalErr)
				assert.JSONEq(t, orNull(wantJSON), orNull(got), "column %s", column)
			}
			for _, column := range tt.columns {
				note := columnNote(res, "RC-1", column)
				assert.Contains(t, note, "restored from the mirror", "notes: %v", res.Notes)
				assert.Contains(t, note, mirrorPath)
				assert.NotContains(t, note, "dropped")
				for _, w := range tt.wantNotes {
					assert.Contains(t, note, w)
				}
			}
			assert.Empty(t, columnNote(res, "RC-2", "annotations"), "RC-2 is readable")
			importRoundTrip(t, res.Export)
		})
	}
}

// orNull maps the "" jsonField returns for an omitted key to JSON null,
// so an omitted column compares as empty.
func orNull(raw string) string {
	if raw == "" {
		return "null"
	}
	return raw
}

// TestRecover_UnreadableColumnNoUsableMirrorCopy_DropsColumn is the
// control (MTIX-95.31.3): when the mirror has no usable copy of the node,
// recover keeps salvaging the node without the unreadable column, and the
// note says the column was dropped and why the mirror was not used.
func TestRecover_UnreadableColumnNoUsableMirrorCopy_DropsColumn(t *testing.T) {
	tests := []struct {
		name       string
		noMirror   bool
		edit       func(t *testing.T, m *ExportData)
		wantReason string
	}{
		{name: "no mirror file", noMirror: true, wantReason: "no usable mirror"},
		{name: "mirror without the node", edit: func(t *testing.T, m *ExportData) {
			kept := m.Nodes[:0]
			for _, n := range m.Nodes {
				if n.ID != "RC-1" {
					kept = append(kept, n)
				}
			}
			m.Nodes = kept
		}, wantReason: "holds no copy of this node"},
		{name: "a different task under that id", edit: func(t *testing.T, m *ExportData) {
			mirrorNodeOf(t, m, "RC-1").UID = rcOtherUID
		}, wantReason: "different task"},
		{name: "invalid copy (comment time outside 1..9999)", edit: func(t *testing.T, m *ExportData) {
			mirrorNodeOf(t, m, "RC-1").Annotations[0].CreatedAt = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
		}, wantReason: "does not validate"},
		{name: "mirror from before the column was exported (1.0.0)", edit: func(t *testing.T, m *ExportData) {
			m.SchemaVersion = "1.0.0"
		}, wantReason: `schema_version "1.0.0"`},
		{name: "two copies of the node in the mirror", edit: func(t *testing.T, m *ExportData) {
			m.Nodes = append(m.Nodes, *mirrorNodeOf(t, m, "RC-1"))
		}, wantReason: "2 copies"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, dbPath := seedMirrorColumnFixture(t)
			mirrorPath, _ := exportMirrorOf(t, s, tt.edit, false)
			if tt.noMirror {
				mirrorPath = filepath.Join(t.TempDir(), "absent-tasks.json")
			}
			corruptAndClose(t, s, "annotations")

			res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
			require.NoError(t, err)

			assert.Contains(t, res.RecoveredIDs, "RC-1", "the node is still salvaged")
			assert.NotContains(t, res.LostIDs, "RC-1")
			rc1 := recoveredNode(t, res, "RC-1")
			assert.Empty(t, rc1.Annotations, "no usable copy: the column is dropped")
			assert.NotEmpty(t, rc1.Activity, "the readable columns are kept")

			note := columnNote(res, "RC-1", "annotations")
			assert.Contains(t, note, "dropped", "notes: %v", res.Notes)
			assert.Contains(t, note, tt.wantReason)
			assert.NotContains(t, note, "restored")
			importRoundTrip(t, res.Export)
		})
	}
}
