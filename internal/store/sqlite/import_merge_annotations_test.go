// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.1 (FR-7.8): a merge import never drops local
// annotations. Annotations merge as a union by annotation id; for the same
// id the local copy wins unless the incoming copy is resolved and the local
// one is not, so a resolution never regresses. Written red-first.
package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Annotation ids and summaries of the fixture (see columnTestAnnotations).
const (
	annA    = "01J9ANNOTATION0000000000AA"
	annB    = "01J9ANNOTATION0000000000BB"
	annC    = "01J9ANNOTATION0000000000CC"
	annOld  = "01J9ANNOTATION00000000000O"
	sumA    = annA + ":resolved:PASS: <evidence> & receipt"
	sumB    = annB + ":open:please re-run the bar"
	sumBRes = annB + ":resolved:please re-run the bar"
	sumC    = annC + ":open:incoming comment"
	sumOld  = annOld + ":open:older upstream comment"
)

// incomingAnnotationC is an annotation only the incoming file carries.
func incomingAnnotationC() map[string]any {
	return map[string]any{
		"id": annC, "author": "teammate", "text": "incoming comment",
		"created_at": "2026-09-01T10:03:00Z", "resolved": false,
	}
}

// changeContent edits node id so its content hash differs from the local
// one, which sends the merge down the update path.
func changeContent(t *testing.T, doc map[string]any, id string) {
	t.Helper()
	n := docNode(t, doc, id)
	n["title"] = "Changed upstream"
	n["content_hash"] = "hash-changed-upstream"
}

// TestImport_Merge_AnnotationsNeverDropped verifies the merge rule on both
// merge paths (unchanged content skips the node's fields, changed content
// updates them): an incoming node without annotations keeps the local ones,
// annotations merge as a union by id, the local copy of an id wins, and a
// resolution never regresses (MTIX-95.31.1).
func TestImport_Merge_AnnotationsNeverDropped(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, doc map[string]any)
		want []string
	}{
		{"legacy 1.0.0 file without annotations", func(t *testing.T, doc map[string]any) {
			asLegacyV100(t, doc)
		}, []string{sumA, sumB}},
		{"current file with the annotations key removed", func(t *testing.T, doc map[string]any) {
			delete(docNode(t, doc, "M-1"), "annotations")
		}, []string{sumA, sumB}},
		{"incoming adds an annotation", func(t *testing.T, doc map[string]any) {
			n := docNode(t, doc, "M-1")
			n["annotations"] = append(docAnnotations(t, doc, "M-1"), incomingAnnotationC())
		}, []string{sumA, sumB, sumC}},
		{"incoming carries only the new annotation", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "M-1")["annotations"] = []any{incomingAnnotationC()}
		}, []string{sumA, sumB, sumC}},
		{"incoming lists the new annotation twice", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "M-1")["annotations"] = []any{incomingAnnotationC(), incomingAnnotationC()}
		}, []string{sumA, sumB, sumC}},
		{"incoming annotation older than the local ones sorts first", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "M-1")["annotations"] = []any{map[string]any{
				"id": annOld, "author": "teammate", "text": "older upstream comment",
				"created_at": "2026-09-01T09:00:00Z", "resolved": false,
			}}
		}, []string{sumOld, sumA, sumB}},
		{"same id with other text keeps the local copy", func(t *testing.T, doc map[string]any) {
			anns := docAnnotations(t, doc, "M-1")
			anns[1].(map[string]any)["text"] = "rewritten upstream"
		}, []string{sumA, sumB}},
		{"incoming resolution of a local open annotation is taken", func(t *testing.T, doc map[string]any) {
			anns := docAnnotations(t, doc, "M-1")
			anns[1].(map[string]any)["resolved"] = true
		}, []string{sumA, sumBRes}},
		{"incoming open copy never un-resolves a local resolution", func(t *testing.T, doc map[string]any) {
			anns := docAnnotations(t, doc, "M-1")
			anns[0].(map[string]any)["resolved"] = false
		}, []string{sumA, sumB}},
	}
	for _, tt := range tests {
		for _, changed := range []bool{false, true} {
			name := tt.name + "/unchanged content"
			if changed {
				name = tt.name + "/changed content"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				s := newTestStore(t)
				createAnnotatedNode(t, s, "M-1", 1)
				data, err := s.Export(ctx, "", "")
				require.NoError(t, err)
				incoming := editExportResealed(t, data, func(doc map[string]any) {
					tt.edit(t, doc)
					if changed {
						changeContent(t, doc, "M-1")
					}
				})

				_, err = s.Import(ctx, incoming, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
				assert.Equal(t, tt.want, annotationSummary(t, s, "M-1"))
			})
		}
	}
}

// TestImport_MergeTwice_AnnotationsNotDuplicated verifies that merging the
// same file again adds nothing: the union is idempotent, and the second
// merge reports the node as skipped (MTIX-95.31.1, FR-7.7a).
func TestImport_MergeTwice_AnnotationsNotDuplicated(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createAnnotatedNode(t, s, "M-1", 1)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	incoming := editExportResealed(t, data, func(doc map[string]any) {
		n := docNode(t, doc, "M-1")
		n["annotations"] = append(docAnnotations(t, doc, "M-1"), incomingAnnotationC())
	})

	first, err := s.Import(ctx, incoming, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	assert.Equal(t, 1, first.NodesUpdated, "adding an annotation is an update")
	second, err := s.Import(ctx, incoming, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	assert.Equal(t, 1, second.NodesSkipped, "nothing is left to merge the second time")
	assert.Equal(t, []string{sumA, sumB, sumC}, annotationSummary(t, s, "M-1"))
}

// TestImport_Merge_ActivityUnion verifies that the activity stream merges
// like annotations: a file without it keeps the local entries, and an entry
// only the file carries is added once (MTIX-95.31.1).
func TestImport_Merge_ActivityUnion(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createAnnotatedNode(t, s, "M-1", 1)
	local, err := s.GetActivity(ctx, "M-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, local, 2)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	legacy := editExportResealed(t, data, func(doc map[string]any) {
		asLegacyV100(t, doc)
		changeContent(t, doc, "M-1")
	})
	_, err = s.Import(ctx, legacy, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := s.GetActivity(ctx, "M-1", 0, 0)
	require.NoError(t, err)
	assert.Len(t, got, 2, "a file without activity keeps the local entries")

	added := editExportResealed(t, data, func(doc map[string]any) {
		n := docNode(t, doc, "M-1")
		act, ok := n["activity"].([]any)
		require.True(t, ok, "export carries no activity")
		n["activity"] = append(act, map[string]any{
			"id": "act-upstream", "type": "comment", "author": "teammate",
			"text": "from upstream", "created_at": "2026-09-01T09:00:00Z",
		})
	})
	for range 2 {
		_, err = s.Import(ctx, added, sqlite.ImportModeMerge, false)
		require.NoError(t, err)
	}
	got, err = s.GetActivity(ctx, "M-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "act-upstream", got[0].ID, "entries are kept in time order")
}

// TestImport_MergeLegacyFile_KeepsLocalNodeColumns verifies that merging a
// schema 1.0.0 file, which carries none of the 2.0.0 columns, leaves the
// local values of those columns as they were, even when the node's content
// changed upstream (MTIX-95.31.1).
func TestImport_MergeLegacyFile_KeepsLocalNodeColumns(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createAnnotatedNode(t, s, "M-1", 1)
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET invalidated_at = ?, invalidated_by = ?, invalidation_reason = ?,
		        deleted_by = ?, metadata = ?, session_id = ?, estimate_min = ?,
		        actual_min = ?, code_refs = ?, commit_refs = ?
		 WHERE id = ?`,
		"2026-09-01T12:00:00Z", "reviewer", "stale", "nobody", `{"k":1}`, "sess-1", 15,
		20, `[{"file":"a.go"}]`, `["abc"]`, "M-1")
	require.NoError(t, err)
	before := nodeColumnRows(t, s)[0]
	for _, col := range legacyColumnKeys() {
		require.NotNil(t, before[col], "fixture must set nodes.%s", col)
	}
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	legacy := editExportResealed(t, data, func(doc map[string]any) {
		asLegacyV100(t, doc)
		changeContent(t, doc, "M-1")
	})

	_, err = s.Import(ctx, legacy, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	after := nodeColumnRows(t, s)[0]
	assert.Equal(t, "Changed upstream", after["title"])
	for _, col := range legacyColumnKeys() {
		assert.Equal(t, before[col], after[col], "a 1.0.0 file must not change nodes.%s", col)
	}
}

// TestImport_MergeCurrentFile_AppliesIncomingColumns verifies that a schema
// 2.0.0 file whose node content changed upstream writes the incoming values
// of the 2.0.0 columns, as it does for every other exported column
// (MTIX-95.31.1).
func TestImport_MergeCurrentFile_AppliesIncomingColumns(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createAnnotatedNode(t, s, "M-1", 1)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	upstream := map[string]any{
		"previous_status":     string(model.StatusOpen),
		"estimate_min":        int64(99),
		"actual_min":          int64(77),
		"code_refs":           `[{"file":"b.go","line":7}]`,
		"commit_refs":         `["fff000"]`,
		"invalidated_at":      "2026-09-02T10:00:00Z",
		"invalidated_by":      "upstream-reviewer",
		"invalidation_reason": "requirements changed",
		"deleted_by":          "upstream-deleter",
		"metadata":            `{"x":2}`,
		"session_id":          "session-upstream",
	}
	incoming := editExportResealed(t, data, func(doc map[string]any) {
		changeContent(t, doc, "M-1")
		n := docNode(t, doc, "M-1")
		for key, value := range upstream {
			n[key] = value
			if text, isText := value.(string); isText && (key == "code_refs" || key == "commit_refs") {
				var decoded any
				require.NoError(t, json.Unmarshal([]byte(text), &decoded))
				n[key] = decoded
			}
		}
	})

	_, err = s.Import(ctx, incoming, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	after := nodeColumnRows(t, s)[0]
	for key, want := range upstream {
		assert.Equal(t, want, after[key], "a 2.0.0 file must write nodes.%s", key)
	}
	assert.Equal(t, []string{sumA, sumB}, annotationSummary(t, s, "M-1"))
}

// TestImport_Merge_NewActivityEntry_WrittenByOneImport verifies, on both
// merge paths, that a single import writes an activity entry only the file
// carries: the update path (content hash changed) and the path that writes
// only the merged annotations and activity (content hash unchanged)
// (MTIX-95.31.1).
func TestImport_Merge_NewActivityEntry_WrittenByOneImport(t *testing.T) {
	for _, changed := range []bool{true, false} {
		name := "unchanged content hash"
		if changed {
			name = "changed content hash"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestStore(t)
			createAnnotatedNode(t, s, "M-1", 1)
			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			incoming := editExportResealed(t, data, func(doc map[string]any) {
				n := docNode(t, doc, "M-1")
				act, ok := n["activity"].([]any)
				require.True(t, ok, "export carries no activity")
				n["activity"] = append(act, map[string]any{
					"id": "act-upstream", "type": "comment", "author": "teammate",
					"text": "from upstream", "created_at": "2026-09-01T09:30:00Z",
				})
				if changed {
					changeContent(t, doc, "M-1")
				}
			})

			result, err := s.Import(ctx, incoming, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			assert.Equal(t, 1, result.NodesUpdated)
			got, err := s.GetActivity(ctx, "M-1", 0, 0)
			require.NoError(t, err)
			require.Len(t, got, 3, "the local entries and the incoming one")
			assert.Equal(t, "act-upstream", got[0].ID, "the oldest entry sorts first")
			assert.Equal(t, "from upstream", got[0].Text)
		})
	}
}
