// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// outOfOrderAnnotations returns annotations newest-first with equal timestamps
// whose ids are out of order.
func outOfOrderAnnotations() []model.Annotation {
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	return []model.Annotation{
		{ID: "D", Text: "d", CreatedAt: at.Add(time.Hour)},
		{ID: "C", Text: "c", CreatedAt: at},
		{ID: "B", Text: "b", CreatedAt: at},
		{ID: "A", Text: "a", CreatedAt: at.Add(-time.Hour)},
	}
}

func TestSetAnnotations_OutOfOrder_StoredInCanonicalOrder(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	in := outOfOrderAnnotations()
	require.NoError(t, s.SetAnnotations(context.Background(), "MTIX-1", in))

	want := annotationIDs(model.SortAnnotations(in))
	var stored []model.Annotation
	require.NoError(t, json.Unmarshal([]byte(readNodeColumn(t, raw, "MTIX-1", "annotations")), &stored))
	require.Equal(t, want, annotationIDs(stored), "stored order is (created_at, id)")
}

func TestAppendAnnotation_OlderThanStored_StoredInCanonicalOrder(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	at := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "B", CreatedAt: at}))
	require.NoError(t, s.AppendAnnotation(ctx, "MTIX-1", model.Annotation{ID: "A", CreatedAt: at}))

	// The raw column: GetNode sorts on read and would hide an unsorted write.
	require.Equal(t, []string{"A", "B"}, rawAnnotationIDs(t, raw, "MTIX-1"))
}

// rawAnnotationIDs returns the annotation ids in the node's stored column,
// in stored order (not through GetNode, which sorts on read).
func rawAnnotationIDs(t *testing.T, raw *sql.DB, id string) []string {
	t.Helper()
	var stored []model.Annotation
	require.NoError(t, json.Unmarshal([]byte(readNodeColumn(t, raw, id, "annotations")), &stored))
	return annotationIDs(stored)
}

// storeInsertionOrder writes outOfOrderAnnotations to the node's column in
// insertion order, as a row saved before MTIX-100.2 holds them.
func storeInsertionOrder(t *testing.T, s *Store, id string) {
	t.Helper()
	stored, err := json.Marshal(outOfOrderAnnotations())
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET annotations = ? WHERE id = ?`, string(stored), id)
	require.NoError(t, err)
}

func TestResolveAnnotation_StoredOutOfOrder_StoredInCanonicalOrder(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	storeInsertionOrder(t, s, "MTIX-1")
	require.Equal(t, []string{"D", "C", "B", "A"}, rawAnnotationIDs(t, raw, "MTIX-1"))

	require.NoError(t, s.ResolveAnnotation(context.Background(), "MTIX-1", "C", true))
	require.Equal(t, []string{"A", "B", "C", "D"}, rawAnnotationIDs(t, raw, "MTIX-1"))
}

func TestApply_Comment_OrderMatchesSortAnnotations(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	storeInsertionOrder(t, s, "MTIX-1")
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpComment, "MTIX-1", "alice", 2,
		&model.CommentPayload{AuthorID: "alice", Body: "synced"})))

	var stored []model.Annotation
	require.NoError(t, json.Unmarshal([]byte(readNodeColumn(t, raw, "MTIX-1", "annotations")), &stored))
	require.Len(t, stored, 5)
	require.Equal(t, annotationIDs(model.SortAnnotations(stored)), annotationIDs(stored),
		"apply stores the raw column in the order SortAnnotations defines")
	require.Equal(t, []string{"A", "B", "C"}, annotationIDs(stored)[:3], "equal times are ordered by id")
}

// TestCreateNode_AnnotationsOutOfOrder_StoredInCanonicalOrder verifies
// CreateNode stores the canonical order in the raw column (MTIX-100.2).
func TestCreateNode_AnnotationsOutOfOrder_StoredInCanonicalOrder(t *testing.T) {
	s, raw := applyTestStore(t)
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: "MTIX-1", Project: "MTIX", Seq: 1, Title: "x", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeForDepth(0),
		ContentHash: "h", CreatedAt: now, UpdatedAt: now, Annotations: outOfOrderAnnotations(),
	}))
	require.Equal(t, []string{"A", "B", "C", "D"}, rawAnnotationIDs(t, raw, "MTIX-1"))
}

// legacyOrderFile puts the annotations of the file's first node in
// insertion order, as a file written by an older mtix lists them, and
// refreshes the checksum.
func legacyOrderFile(t *testing.T, data *ExportData) {
	t.Helper()
	data.Nodes[0].Annotations = outOfOrderAnnotations()
	require.NoError(t, RecomputeExportChecksum(data))
}

// TestImport_AnnotationsStoredOutOfOrder_StoredInCanonicalOrder verifies the
// import insert path stores the canonical order in the raw column, whatever
// order the file lists the annotations in.
func TestImport_AnnotationsStoredOutOfOrder_StoredInCanonicalOrder(t *testing.T) {
	ctx := context.Background()
	src, _ := applyTestStore(t)
	require.NoError(t, applyOnce(t, src, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	data, err := src.Export(ctx, "MTIX", "0.1.0")
	require.NoError(t, err)
	legacyOrderFile(t, data) // a file an older mtix wrote
	dst, raw := applyTestStore(t)
	_, err = dst.Import(ctx, data, ImportModeReplace, true)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C", "D"}, rawAnnotationIDs(t, raw, "MTIX-1"))
}

// TestGetNode_AnnotationsStoredInInsertionOrder_ListedCanonically verifies a
// row saved before MTIX-100.2, with annotations in insertion order, lists in
// (created_at, id) order when the node is read.
func TestGetNode_AnnotationsStoredInInsertionOrder_ListedCanonically(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	stored, err := json.Marshal(outOfOrderAnnotations())
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `UPDATE nodes SET annotations = ? WHERE id = ?`, string(stored), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []string{"D", "C", "B", "A"},
		annotationIDs(func() []model.Annotation {
			var out []model.Annotation
			require.NoError(t, json.Unmarshal([]byte(readNodeColumn(t, raw, "MTIX-1", "annotations")), &out))
			return out
		}()), "the row holds the old insertion order")

	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B", "C", "D"}, annotationIDs(n.Annotations))
}

// exportJSONNoTime returns the store's export as JSON without its export time.
func exportJSONNoTime(t *testing.T, s *Store) string {
	t.Helper()
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	data.ExportedAt = ""
	b, err := json.Marshal(data)
	require.NoError(t, err)
	return string(b)
}

// TestImport_ReplaceOfUnchangedFile_LegacyOrderedRow_IsNoOp verifies a replace
// import (the mode sync auto-import uses) of an unchanged export over a row
// whose annotations are stored out of order writes no history entry and
// leaves the export byte-identical (MTIX-100.2).
func TestImport_ReplaceOfUnchangedFile_LegacyOrderedRow_IsNoOp(t *testing.T) {
	ctx := context.Background()
	s, _ := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	storeInsertionOrder(t, s, "MTIX-1")
	var before int
	require.NoError(t, s.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM task_history`).Scan(&before))
	first := exportJSONNoTime(t, s)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	_, err = s.Import(ctx, data, ImportModeReplace, true)
	require.NoError(t, err)

	var after int
	require.NoError(t, s.ReadDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM task_history`).Scan(&after))
	require.Equal(t, before, after, "an unchanged file records no history entry")
	require.Equal(t, first, exportJSONNoTime(t, s), "the export is byte-identical")
}

// mergeFixture returns a store dst holding MTIX-1 with legacy-ordered
// annotations, and an export of a copy of the same task (same uid) after
// change ran on that copy's row, for a merge import into dst.
func mergeFixture(t *testing.T, change string) (*Store, *sql.DB, *ExportData) {
	t.Helper()
	ctx := context.Background()
	dst, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, dst, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	storeInsertionOrder(t, dst, "MTIX-1")
	base, err := dst.Export(ctx, "", "")
	require.NoError(t, err)
	src, _ := applyTestStore(t)
	_, err = src.Import(ctx, base, ImportModeReplace, true)
	require.NoError(t, err)
	_, err = src.WriteDB().ExecContext(ctx, change)
	require.NoError(t, err)
	data, err := src.Export(ctx, "", "")
	require.NoError(t, err)
	legacyOrderFile(t, data)
	return dst, raw, data
}

// TestImport_MergeUpdatePath_StoresCanonicalAnnotationOrder verifies a merge
// import that takes the file's newer content stores the annotations in
// canonical order in the raw column (updateExportNode), though the union
// kept the local list unchanged.
func TestImport_MergeUpdatePath_StoresCanonicalAnnotationOrder(t *testing.T) {
	dst, raw, data := mergeFixture(t,
		`UPDATE nodes SET title = 'changed', content_hash = 'h-changed', updated_at = '2099-01-01T00:00:00Z' WHERE id = 'MTIX-1'`)
	_, err := dst.Import(context.Background(), data, ImportModeMerge, true)
	require.NoError(t, err)
	n, err := dst.GetNode(context.Background(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, "changed", n.Title, "the update path ran")
	require.Equal(t, []string{"A", "B", "C", "D"}, rawAnnotationIDs(t, raw, "MTIX-1"))
}

// TestImport_MergeStreamsPath_StoresCanonicalAnnotationOrder verifies a merge
// import that changes only the activity stream stores the annotations in
// canonical order in the raw column (writeNodeStreams).
func TestImport_MergeStreamsPath_StoresCanonicalAnnotationOrder(t *testing.T) {
	dst, raw, data := mergeFixture(t,
		`UPDATE nodes SET activity = '[{"id":"ACT-NEW","type":"comment","author":"bob","created_at":"2026-09-02T09:00:00Z"}]' WHERE id = 'MTIX-1'`)
	_, err := dst.Import(context.Background(), data, ImportModeMerge, true)
	require.NoError(t, err)
	require.Contains(t, readNodeColumn(t, raw, "MTIX-1", "activity"), "ACT-NEW", "the streams path ran")
	require.Equal(t, []string{"A", "B", "C", "D"}, rawAnnotationIDs(t, raw, "MTIX-1"))
}
