// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.1 (FR-15.1, FR-7.8): mtix export and .mtix/tasks.json
// carry every annotation of every node, in the structure mtix show --json
// returns, covered by the canonical checksum, and every nodes column is
// either exported or listed as deliberately excluded. Written red-first.
package sqlite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// nodesTableColumns lists the columns of the nodes table, in schema order.
func nodesTableColumns(t *testing.T, s *sqlite.Store) []string {
	t.Helper()
	rows, err := s.ReadDB().QueryContext(context.Background(),
		`SELECT name FROM pragma_table_info('nodes') ORDER BY cid`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var cols []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		cols = append(cols, name)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, cols)
	return cols
}

// TestExport_EveryNodesColumn_ExportedOrListedAsExcluded verifies that the
// exported node carries a same-named key for every column of the nodes
// table, read from the live schema so a column added later cannot be
// silently left out (MTIX-95.31.1). The excluded map must match the list
// above exportNode in export.go: it is empty because every column is node
// data.
func TestExport_EveryNodesColumn_ExportedOrListedAsExcluded(t *testing.T) {
	s := newTestStore(t)
	createEveryColumnNode(t, s, "COL-1", 1)

	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	node := exportedNode(t, data, "COL-1")

	excluded := map[string]string{} // column -> reason it is not exported
	for _, col := range nodesTableColumns(t, s) {
		if _, ok := excluded[col]; ok {
			continue
		}
		assert.Contains(t, node, col,
			"nodes.%s is neither exported nor listed as deliberately excluded", col)
	}
}

// TestExport_Annotations_SameStructureAsShowJSON verifies that a node's
// exported annotations are exactly what mtix show --json returns for it
// (the JSON of the stored model.Node): every annotation, with its id,
// author, text, time, resolution and addressee (MTIX-95.31.1).
func TestExport_Annotations_SameStructureAsShowJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createAnnotatedNode(t, s, "ANN-1", 1)
	createAnnotatedNode(t, s, "ANN-2", 2)

	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	for _, id := range []string{"ANN-1", "ANN-2"} {
		node, getErr := s.GetNode(ctx, id)
		require.NoError(t, getErr)
		require.Len(t, node.Annotations, 2)
		showJSON, marshalErr := json.Marshal(node)
		require.NoError(t, marshalErr)
		var show map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(showJSON, &show))

		exported := exportedNode(t, data, id)
		require.Contains(t, exported, "annotations", "export of %s carries no annotations", id)
		assert.JSONEq(t, string(show["annotations"]), string(exported["annotations"]))
	}
}

// TestExport_AnnotationEdits_FailChecksum verifies that the canonical
// checksum covers annotations: a tasks.json whose annotations were edited
// after mtix wrote it fails verification (MTIX-95.31.1, FR-7.8).
func TestExport_AnnotationEdits_FailChecksum(t *testing.T) {
	s := newTestStore(t)
	createAnnotatedNode(t, s, "ANN-1", 1)
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)

	tests := []struct {
		name string
		edit func(anns []any) []any
	}{
		{"text changed", func(a []any) []any { a[0].(map[string]any)["text"] = "FAIL"; return a }},
		{"resolution flipped", func(a []any) []any { a[0].(map[string]any)["resolved"] = false; return a }},
		{"addressee changed", func(a []any) []any { a[1].(map[string]any)["addressee"] = "agent-b"; return a }},
		{"annotation removed", func(a []any) []any { return a[:1] }},
		{"annotation added", func(a []any) []any {
			return append(a, map[string]any{"id": "X", "author": "x", "text": "x",
				"created_at": "2026-09-01T10:00:00Z", "resolved": false})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edited := editExport(t, data, func(doc map[string]any) {
				docNode(t, doc, "ANN-1")["annotations"] = tt.edit(docAnnotations(t, doc, "ANN-1"))
			})
			valid, verifyErr := sqlite.VerifyExportChecksum(edited)
			require.NoError(t, verifyErr)
			assert.False(t, valid, "an edited annotation must fail checksum verification")
		})
	}

	unedited := editExport(t, data, func(map[string]any) {})
	valid, err := sqlite.VerifyExportChecksum(unedited)
	require.NoError(t, err)
	assert.True(t, valid, "the file as written must verify")
}

// TestExport_NewAnnotation_ChangesChecksum verifies that adding a comment to
// an otherwise unchanged board changes the export checksum, so the board of
// record reflects it (MTIX-95.31.1).
func TestExport_NewAnnotation_ChangesChecksum(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createAnnotatedNode(t, s, "ANN-1", 1)
	before, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	anns := append(columnTestAnnotations(), model.Annotation{
		ID: "01J9ANNOTATION0000000000CC", Author: "reviewer", Text: "FAIL: bar red",
		CreatedAt: columnTestTime().Add(3 * time.Minute),
	})
	require.NoError(t, s.SetAnnotations(ctx, "ANN-1", anns))
	after, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	assert.NotEqual(t, before.Checksum, after.Checksum)
	exported := exportedNode(t, after, "ANN-1")
	var got []model.Annotation
	require.NoError(t, json.Unmarshal(exported["annotations"], &got))
	assert.Len(t, got, 3)
}

// TestExport_UnreadableAnnotationsColumn_FailsNamingNodeAndColumn verifies
// that a node whose annotations column does not parse fails the export with
// an error naming the node and the column, instead of writing a board of
// record without that evidence (MTIX-95.31.1).
func TestExport_UnreadableAnnotationsColumn_FailsNamingNodeAndColumn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	createAnnotatedNode(t, s, "ANN-1", 1)
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET annotations = ? WHERE id = ?`, `[{"id": "broken"`, "ANN-1")
	require.NoError(t, err)

	_, err = s.Export(ctx, "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ANN-1")
	assert.Contains(t, err.Error(), "annotations")
}

// legacyV100Export is a schema 1.0.0 export in the exact shape mtix 0.5.3
// wrote, with the checksum 0.5.3 computed for it. It carries no
// annotations and none of the other 1.1.0 columns.
const legacyV100Export = `{
  "version": 1,
  "schema_version": "1.0.0",
  "exported_at": "2026-09-01T10:00:00Z",
  "mtix_version": "0.5.3-beta",
  "project": "",
  "nodes": [
    {
      "id": "LEG-1", "parent_id": "", "depth": 0, "seq": 1, "project": "LEG",
      "title": "Legacy story <with> & markup",
      "description": "Written by mtix 0.5.3 \u2014 schema 1.0.0",
      "prompt": "Do the legacy thing", "acceptance": "It is done",
      "node_type": "epic", "issue_type": "", "priority": 2,
      "labels": "[\"legacy\",\"v1\"]", "status": "in_progress", "progress": 0.5,
      "assignee": "agent-a", "creator": "vimal", "agent_state": "working",
      "weight": 1, "content_hash": "legacy-hash-1",
      "created_at": "2026-08-01T09:00:00Z", "updated_at": "2026-08-02T09:00:00Z",
      "uid": "01J0LEGACYUID0000000000001"
    },
    {
      "id": "LEG-1.1", "parent_id": "LEG-1", "depth": 1, "seq": 1, "project": "LEG",
      "title": "Legacy child", "description": "", "prompt": "", "acceptance": "",
      "node_type": "story", "issue_type": "", "priority": 3, "labels": "[]",
      "status": "done", "progress": 1, "assignee": "", "creator": "",
      "agent_state": "", "weight": 1, "content_hash": "legacy-hash-2",
      "created_at": "2026-08-01T09:30:00Z", "updated_at": "2026-08-03T09:00:00Z",
      "closed_at": "2026-08-03T09:00:00Z", "uid": "01J0LEGACYUID0000000000002"
    }
  ],
  "dependencies": [
    {"from_id": "LEG-1.1", "to_id": "LEG-1", "dep_type": "related",
     "created_at": "2026-08-01T10:00:00Z"}
  ],
  "agents": [],
  "sessions": [],
  "node_count": 2,
  "checksum": "1a06ba6fb106363c995a85a41c1d2164e607021159d75295c8269a5ed12f7386"
}`

// TestVerifyExportChecksum_SchemaV100File_VerifiesAndImports verifies that a
// file written before schema 1.1.0 still verifies against the checksum its
// writer computed, and imports in both modes (MTIX-95.31.1): the 1.1.0
// columns are omitted when empty, so an annotation-free node hashes exactly
// as it did.
func TestVerifyExportChecksum_SchemaV100File_VerifiesAndImports(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
		t.Run(string(mode), func(t *testing.T) {
			data, err := sqlite.DecodeExportData(bytes.NewReader([]byte(legacyV100Export)))
			require.NoError(t, err)
			valid, err := sqlite.VerifyExportChecksum(data)
			require.NoError(t, err)
			require.True(t, valid, "a schema 1.0.0 file must verify against its original checksum")

			s := newTestStore(t)
			_, err = s.Import(context.Background(), data, mode, false)
			require.NoError(t, err)
			node, err := s.GetNode(context.Background(), "LEG-1")
			require.NoError(t, err)
			assert.Equal(t, "Legacy story <with> & markup", node.Title)
			assert.Equal(t, []string{"legacy", "v1"}, node.Labels)
			assert.Empty(t, node.Annotations)
		})
	}
}
