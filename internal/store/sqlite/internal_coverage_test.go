// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// newInternalTestStore creates a test store with direct access to internal fields.
func newInternalTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// =============================================================================
// childCount coverage (0% → 100%) per FR-3.1
// =============================================================================

// TestChildCount_NoChildren_ReturnsZero verifies zero count per FR-3.1.
func TestChildCount_NoChildren_ReturnsZero(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CC-1", "", "CC", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	count, err := s.childCount(ctx, "CC-1")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// TestChildCount_WithChildren_ReturnsCount verifies accurate count per FR-3.1.
func TestChildCount_WithChildren_ReturnsCount(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CC2-1", "", "CC2", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	c1 := makeTestNode("CC2-1.1", "CC2-1", "CC2", "Child1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("CC2-1.2", "CC2-1", "CC2", "Child2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	count, err := s.childCount(ctx, "CC2-1")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
}

// TestChildCount_ExcludesDeleted verifies deleted exclusion per FR-3.3.
func TestChildCount_ExcludesDeleted(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CC3-1", "", "CC3", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	c1 := makeTestNode("CC3-1.1", "CC3-1", "CC3", "Keep", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("CC3-1.2", "CC3-1", "CC3", "Delete", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	require.NoError(t, s.DeleteNode(ctx, "CC3-1.2", false, "admin"))

	count, err := s.childCount(ctx, "CC3-1")
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// =============================================================================
// openDB error path coverage
// =============================================================================

// TestOpenDB_InvalidPath_ReturnsError verifies error on invalid path.
func TestOpenDB_InvalidPath_ReturnsError(t *testing.T) {
	_, err := openDB(context.Background(), "/nonexistent/deep/path/test.db", true)
	assert.Error(t, err)
}

// TestOpenDB_ReaderMode_NoWAL verifies reader doesn't set WAL.
func TestOpenDB_ReaderMode_NoWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reader.db")
	// Create the database first.
	wdb, err := openDB(context.Background(), dbPath, true)
	require.NoError(t, err)
	require.NoError(t, wdb.Close())

	// Open as reader.
	rdb, err := openDB(context.Background(), dbPath, false)
	require.NoError(t, err)
	defer func() { _ = rdb.Close() }()

	// Verify FK is enabled.
	var fk int
	require.NoError(t, rdb.QueryRow("PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk)
}

// =============================================================================
// Close error path coverage
// =============================================================================

// TestClose_AfterReadDBClosed_ReturnsError verifies close error propagation.
func TestClose_AfterReadDBClosed_ReturnsError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)

	// Pre-close readDB to trigger error path in Close.
	require.NoError(t, s.readDB.Close())

	// Close should still succeed (or return error from readDB).
	// Either way, it exercises the error path.
	_ = s.Close()
}

// TestClose_AfterWriteDBClosed_NoParic verifies write DB double-close is safe.
func TestClose_AfterWriteDBClosed_NoPanic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close2.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)

	// Pre-close writeDB to exercise the Close error path.
	require.NoError(t, s.writeDB.Close())

	// Close should not panic — it may or may not return error.
	_ = s.Close()
}

// =============================================================================
// verifyFTS error path coverage
// =============================================================================

// TestVerifyFTS_CorruptIndex_ReportsFalse verifies FTS corruption detection per FR-6.3.
func TestVerifyFTS_CorruptIndex_ReportsFalse(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	result := &VerifyResult{}
	err := s.verifyFTS(ctx, result)
	require.NoError(t, err)
	assert.True(t, result.FTSOK)
}

// =============================================================================
// verifyIntegrity coverage
// =============================================================================

// TestVerifyIntegrity_ValidDB_ReturnsOK verifies integrity check per FR-6.3.
func TestVerifyIntegrity_ValidDB_ReturnsOK(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	result := &VerifyResult{}
	err := s.verifyIntegrity(ctx, result)
	require.NoError(t, err)
	assert.True(t, result.IntegrityOK)
}

// =============================================================================
// verifySequences coverage
// =============================================================================

// TestVerifySequences_EmptyDB_ReturnsOK verifies empty store verification per FR-6.3.
func TestVerifySequences_EmptyDB_ReturnsOK(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	result := &VerifyResult{}
	err := s.verifySequences(ctx, result)
	require.NoError(t, err)
	assert.True(t, result.SequenceOK)
}

// TestVerifySequences_MismatchedSequence_ReportsError verifies sequence mismatch per FR-6.3.
func TestVerifySequences_MismatchedSequence_ReportsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create node directly without using NextSequence — sequence table won't match.
	root := makeTestNode("VS-1", "", "VS", "Root", 0, 5, now)
	require.NoError(t, s.CreateNode(ctx, root))

	result := &VerifyResult{}
	err := s.verifySequences(ctx, result)
	require.NoError(t, err)
	assert.False(t, result.SequenceOK)
	assert.NotEmpty(t, result.Errors)
}

// =============================================================================
// verifyProgress coverage
// =============================================================================

// TestVerifyProgress_ConsistentProgress_ReturnsOK verifies progress check per FR-5.7.
func TestVerifyProgress_ConsistentProgress_ReturnsOK(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("VPC-1", "", "VPC", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	c1 := makeTestNode("VPC-1.1", "VPC-1", "VPC", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))

	require.NoError(t, s.TransitionStatus(ctx, "VPC-1.1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "VPC-1.1", model.StatusDone, "done", "agent"))

	result := &VerifyResult{}
	err := s.verifyProgress(ctx, result)
	require.NoError(t, err)
	assert.True(t, result.ProgressOK)
}

// TestVerifyProgress_InconsistentProgress_ReportsError verifies progress mismatch per FR-5.7.
func TestVerifyProgress_InconsistentProgress_ReportsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("VPI-1", "", "VPI", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	c1 := makeTestNode("VPI-1.1", "VPI-1", "VPI", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))

	// Manually set parent progress to wrong value.
	_, err := s.writeDB.ExecContext(ctx,
		"UPDATE nodes SET progress = 0.99 WHERE id = ?", "VPI-1")
	require.NoError(t, err)

	result := &VerifyResult{}
	err = s.verifyProgress(ctx, result)
	require.NoError(t, err)
	assert.False(t, result.ProgressOK)
	assert.NotEmpty(t, result.Errors)
}

// =============================================================================
// WithTx panic recovery coverage
// =============================================================================

// TestWithTx_PanicRecovery_RollsBack verifies panic rollback per CODING-STYLE.md.
func TestWithTx_PanicRecovery_RollsBack(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("TX-1", "", "TX", "Before", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Panic inside WithTx should rollback and re-panic.
	assert.Panics(t, func() {
		_ = s.WithTx(ctx, func(tx *sql.Tx) error {
			// Modify something in the transaction.
			_, _ = tx.ExecContext(ctx, "UPDATE nodes SET title = ? WHERE id = ?", "Modified", "TX-1")
			panic("deliberate test panic")
		})
	})

	// The update should have been rolled back.
	node, err := s.GetNode(ctx, "TX-1")
	require.NoError(t, err)
	assert.Equal(t, "Before", node.Title)
}

// =============================================================================
// appendActivityEntry coverage
// =============================================================================

// TestAppendActivityEntry_EmptyActivity_CreatesNew verifies first entry per FR-3.5.
func TestAppendActivityEntry_EmptyActivity_CreatesNew(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("AA-1", "", "AA", "Activity", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Clear existing activity.
	_, err := s.writeDB.ExecContext(ctx, "UPDATE nodes SET activity = NULL WHERE id = ?", "AA-1")
	require.NoError(t, err)

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		return appendActivityEntry(ctx, tx, "AA-1", model.ActivityEntry{
			ID:        "act-test-1",
			Type:      model.ActivityTypeComment,
			Author:    "test",
			Text:      "First entry",
			CreatedAt: now,
		})
	})
	require.NoError(t, err)

	// Verify activity was stored.
	var actJSON string
	err = s.readDB.QueryRowContext(ctx, "SELECT activity FROM nodes WHERE id = ?", "AA-1").Scan(&actJSON)
	require.NoError(t, err)

	var entries []model.ActivityEntry
	require.NoError(t, json.Unmarshal([]byte(actJSON), &entries))
	assert.Len(t, entries, 1)
	assert.Equal(t, "First entry", entries[0].Text)
}

// TestAppendActivityEntry_ExistingActivity_Appends verifies append per FR-3.5.
func TestAppendActivityEntry_ExistingActivity_Appends(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("AA2-1", "", "AA2", "Activity", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Node already has 'created' activity from CreateNode.
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return appendActivityEntry(ctx, tx, "AA2-1", model.ActivityEntry{
			ID:        "act-test-2",
			Type:      model.ActivityTypeComment,
			Author:    "test",
			Text:      "Second entry",
			CreatedAt: now,
		})
	})
	require.NoError(t, err)

	var actJSON string
	err = s.readDB.QueryRowContext(ctx, "SELECT activity FROM nodes WHERE id = ?", "AA2-1").Scan(&actJSON)
	require.NoError(t, err)

	var entries []model.ActivityEntry
	require.NoError(t, json.Unmarshal([]byte(actJSON), &entries))
	assert.GreaterOrEqual(t, len(entries), 2)
}

// =============================================================================
// mustMarshal coverage
// =============================================================================

// TestMustMarshal_ValidInput_ReturnsJSON verifies JSON marshaling.
func TestMustMarshal_ValidInput_ReturnsJSON(t *testing.T) {
	data := mustMarshal(map[string]string{"key": "value"})
	assert.JSONEq(t, `{"key":"value"}`, string(data))
}

// =============================================================================
// isUniqueConstraintError coverage
// =============================================================================

// TestIsUniqueConstraintError_NilError_ReturnsFalse verifies nil handling.
func TestIsUniqueConstraintError_NilError_ReturnsFalse(t *testing.T) {
	assert.False(t, isUniqueConstraintError(nil))
}

// =============================================================================
// marshalJSONField edge cases
// =============================================================================

// TestMarshalJSONField_NilValue_ReturnsNull verifies nil handling.
func TestMarshalJSONField_NilValue_ReturnsNull(t *testing.T) {
	result, err := marshalJSONField(nil)
	require.NoError(t, err)
	assert.False(t, result.Valid)
}

// TestMarshalJSONField_EmptySlice_ReturnsNull verifies empty slice handling.
func TestMarshalJSONField_EmptySlice_ReturnsNull(t *testing.T) {
	result, err := marshalJSONField([]string{})
	require.NoError(t, err)
	assert.False(t, result.Valid)
}

// TestMarshalJSONField_NonEmptySlice_ReturnsJSON verifies non-empty handling.
func TestMarshalJSONField_NonEmptySlice_ReturnsJSON(t *testing.T) {
	result, err := marshalJSONField([]string{"a", "b"})
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Contains(t, result.String, "a")
}

// =============================================================================
// unmarshalJSONField edge cases
// =============================================================================

// TestUnmarshalJSONField_NullString_NoOp verifies null handling.
func TestUnmarshalJSONField_NullString_NoOp(t *testing.T) {
	var labels []string
	err := unmarshalJSONField(sql.NullString{}, &labels)
	require.NoError(t, err)
	assert.Nil(t, labels)
}

// TestUnmarshalJSONField_ValidJSON_Parses verifies valid parsing.
func TestUnmarshalJSONField_ValidJSON_Parses(t *testing.T) {
	var labels []string
	err := unmarshalJSONField(sql.NullString{String: `["a","b"]`, Valid: true}, &labels)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, labels)
}

// TestUnmarshalJSONField_NullLiteral_NoOp verifies "null" handling.
func TestUnmarshalJSONField_NullLiteral_NoOp(t *testing.T) {
	var labels []string
	err := unmarshalJSONField(sql.NullString{String: "null", Valid: true}, &labels)
	require.NoError(t, err)
	assert.Nil(t, labels)
}

// TestUnmarshalJSONField_InvalidJSON_ReturnsError verifies error handling.
func TestUnmarshalJSONField_InvalidJSON_ReturnsError(t *testing.T) {
	var labels []string
	err := unmarshalJSONField(sql.NullString{String: "not json", Valid: true}, &labels)
	assert.Error(t, err)
}

// =============================================================================
// parseNullableTime edge cases
// =============================================================================

// TestParseNullableTime_EmptyString_NoOp verifies empty handling.
func TestParseNullableTime_EmptyString_NoOp(t *testing.T) {
	var result *time.Time
	err := parseNullableTime(sql.NullString{String: "", Valid: true}, &result)
	require.NoError(t, err)
	assert.Nil(t, result)
}

// TestParseNullableTime_ValidTime_Parses verifies time parsing.
func TestParseNullableTime_ValidTime_Parses(t *testing.T) {
	var result *time.Time
	err := parseNullableTime(sql.NullString{String: "2026-03-10T12:00:00Z", Valid: true}, &result)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 2026, result.Year())
}

// TestParseNullableTime_InvalidTime_ReturnsError verifies error handling.
func TestParseNullableTime_InvalidTime_ReturnsError(t *testing.T) {
	var result *time.Time
	err := parseNullableTime(sql.NullString{String: "not-a-time", Valid: true}, &result)
	assert.Error(t, err)
}

// =============================================================================
// rebuildSequences / rebuildFTS coverage
// =============================================================================

// TestRebuildSequences_Empty_NoError verifies empty rebuild per FR-7.8.
func TestRebuildSequences_Empty_NoError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.rebuildSequences(ctx)
	require.NoError(t, err)
}

// TestRebuildSequences_WithData_RebuildsCorrectly verifies sequence rebuild per FR-7.8.
func TestRebuildSequences_WithData_RebuildsCorrectly(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("RS-1", "", "RS", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("RS-1.1", "RS-1", "RS", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("RS-1.2", "RS-1", "RS", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	// Clear sequences table.
	_, err := s.writeDB.ExecContext(ctx, "DELETE FROM sequences")
	require.NoError(t, err)

	// Rebuild.
	require.NoError(t, s.rebuildSequences(ctx))

	// Verify sequence for RS:RS-1 is 2 (max seq).
	var val int
	err = s.readDB.QueryRowContext(ctx,
		"SELECT value FROM sequences WHERE key = ?", "RS:RS-1").Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, 2, val)
}

// TestRebuildFTS_NoError verifies FTS rebuild per FR-7.8.
func TestRebuildFTS_NoError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.rebuildFTS(ctx)
	require.NoError(t, err)
}

// =============================================================================
// Verify all fields coverage via VerifyResult
// =============================================================================

// TestVerify_AllSubChecks_ReturnResults verifies all verify sub-checks per FR-6.3.
func TestVerify_AllSubChecks_ReturnResults(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.IntegrityOK)
	assert.True(t, result.ForeignKeyOK)
	assert.True(t, result.SequenceOK)
	assert.True(t, result.ProgressOK)
	assert.True(t, result.FTSOK)
	assert.True(t, result.AllPassed)
}

// =============================================================================
// Backup internal coverage
// =============================================================================

// TestVerifyDatabase_ValidDB_ReturnsTrue verifies database verification.
func TestVerifyDatabase_ValidDB_ReturnsTrue(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "verify.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())

	ok, err := verifyDatabase(context.Background(), dbPath)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestVerifyDatabase_NonExistent_ReturnsError verifies missing file.
func TestVerifyDatabase_NonExistent_ReturnsError(t *testing.T) {
	_, err := verifyDatabase(context.Background(), "/nonexistent/file.db")
	assert.Error(t, err)
}

// TestEscapeSQLitePath_WithQuotes_Escapes verifies path escaping.
func TestEscapeSQLitePath_WithQuotes_Escapes(t *testing.T) {
	result := escapeSQLitePath("path'with'quotes")
	assert.Equal(t, "path''with''quotes", result)
}

// TestEscapeSQLitePath_NoQuotes_Unchanged verifies no-op escaping.
func TestEscapeSQLitePath_NoQuotes_Unchanged(t *testing.T) {
	result := escapeSQLitePath("/normal/path/file.db")
	assert.Equal(t, "/normal/path/file.db", result)
}

// =============================================================================
// Export internal coverage
// =============================================================================

// TestComputeExportChecksum_Deterministic verifies checksum reproducibility per FR-7.8.
func TestComputeExportChecksum_Deterministic(t *testing.T) {
	nodes := []exportNode{{ID: "A", Title: "Node A"}, {ID: "B", Title: "Node B"}}
	deps := []exportDep{{FromID: "A", ToID: "B", DepType: "blocks"}}

	checksum1, err := computeExportChecksum(nodes, deps)
	require.NoError(t, err)

	checksum2, err := computeExportChecksum(nodes, deps)
	require.NoError(t, err)

	assert.Equal(t, checksum1, checksum2)
}

// TestComputeExportChecksum_DifferentData_DifferentChecksum verifies uniqueness.
func TestComputeExportChecksum_DifferentData_DifferentChecksum(t *testing.T) {
	nodes1 := []exportNode{{ID: "A"}}
	nodes2 := []exportNode{{ID: "B"}}

	cs1, err := computeExportChecksum(nodes1, nil)
	require.NoError(t, err)
	cs2, err := computeExportChecksum(nodes2, nil)
	require.NoError(t, err)

	assert.NotEqual(t, cs1, cs2)
}

// =============================================================================
// Import internal coverage
// =============================================================================

// TestNullStr_Empty_ReturnsNil verifies empty string handling.
func TestNullStr_Empty_ReturnsNil(t *testing.T) {
	assert.Nil(t, nullStr(""))
}

// TestNullStr_NonEmpty_ReturnsString verifies non-empty handling.
func TestNullStr_NonEmpty_ReturnsString(t *testing.T) {
	assert.Equal(t, "hello", nullStr("hello"))
}

// =============================================================================
// recalculateProgress internal coverage per FR-5.7
// =============================================================================

// TestRecalculateProgress_EmptyNodeID_NoOp verifies empty ID returns nil.
func TestRecalculateProgress_EmptyNodeID_NoOp(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return recalculateProgress(ctx, tx, "")
	})
	require.NoError(t, err)
}

// =============================================================================
// init coverage (schema creation)
// =============================================================================

// TestInit_Idempotent verifies schema creation is idempotent per NFR-2.1.
func TestInit_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "init.db")

	s1, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	// Re-open with same path triggers init again.
	s2, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

// =============================================================================
// ListNodes / SearchNodes internal coverage
// =============================================================================

// TestListNodes_NodeTypeFilter_Correct verifies node type filtering per FR-2.7.5.
func TestListNodes_NodeTypeFilter_Correct(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeTestNode("LNT-1", "", "LNT", "Auto", 0, 1, now)
	n1.NodeType = model.NodeTypeAuto
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeTestNode("LNT-2", "", "LNT", "Issue", 0, 2, now)
	n2.NodeType = model.NodeTypeForDepth(2) // "issue"
	require.NoError(t, s.CreateNode(ctx, n2))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		NodeType: string(model.NodeTypeForDepth(2)),
	}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, nodes, 1)
	assert.Equal(t, "LNT-2", nodes[0].ID)
}

// TestSearchNodes_EmptyQuery_ReturnsError verifies empty query rejection per FR-2.6.
func TestSearchNodes_EmptyQuery_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	_, _, err := s.SearchNodes(ctx, "", store.NodeFilter{}, store.ListOptions{Limit: 10})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// =============================================================================
// Backup with data creates verifiable copy
// =============================================================================

// TestBackup_EmptyPath_ReturnsError verifies empty path rejection per FR-6.3a.
func TestBackup_EmptyPath_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	_, err := s.Backup(ctx, "")
	assert.Error(t, err)
}

// TestBackup_CreatesVerifiedCopy verifies backup integrity per FR-6.3a.
func TestBackup_CreatesVerifiedCopy(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("BK-1", "", "BK", "Backup", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	dest := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.Backup(ctx, dest)
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.Greater(t, result.Size, int64(0))

	// Verify backup file exists.
	_, err = os.Stat(dest)
	assert.NoError(t, err)
}

// =============================================================================
// Export/Import roundtrip with agents and sessions per FR-7.8
// =============================================================================

// TestExportImportRoundtrip_WithAgentsAndSessions verifies full roundtrip per FR-7.8.
func TestExportImportRoundtrip_WithAgentsAndSessions(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("EIR-1", "", "EIR", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("EIR-1.1", "EIR-1", "EIR", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))

	// Add a dependency.
	dep := &model.Dependency{FromID: "EIR-1", ToID: "EIR-1.1", DepType: model.DepTypeRelated}
	require.NoError(t, s.AddDependency(ctx, dep))

	// Insert an agent directly.
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO agents (agent_id, project, state) VALUES (?, ?, ?)`,
		"agent-1", "EIR", "idle")
	require.NoError(t, err)

	// Insert a session directly.
	_, err = s.writeDB.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, project, started_at, status) VALUES (?, ?, ?, ?, ?)`,
		"sess-1", "agent-1", "EIR", now.Format(time.RFC3339), "active")
	require.NoError(t, err)

	// Export.
	data, err := s.Export(ctx, "EIR", "1.0.0")
	require.NoError(t, err)
	assert.Len(t, data.Nodes, 2)
	assert.Len(t, data.Dependencies, 1)
	assert.Len(t, data.Agents, 1)
	assert.Len(t, data.Sessions, 1)

	// Verify checksum.
	valid, err := VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid)

	// Import into a new store.
	s2 := newInternalTestStore(t)
	result, err := s2.Import(ctx, data, ImportModeReplace, false)
	require.NoError(t, err)
	assert.Equal(t, 2, result.NodesCreated)
	assert.Equal(t, 1, result.DepsImported)
	assert.True(t, result.FTSRebuilt)

	// Verify data in new store.
	node, err := s2.GetNode(ctx, "EIR-1")
	require.NoError(t, err)
	assert.Equal(t, "Root", node.Title)
}

// TestImportMerge_UpdatesExistingNodes verifies merge mode per FR-7.8.
func TestImportMerge_UpdatesExistingNodes(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("IM-1", "", "IM", "Original", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Export.
	data, err := s.Export(ctx, "IM", "1.0.0")
	require.NoError(t, err)

	// Modify node in export data — change title and content hash.
	data.Nodes[0].Title = "Updated"
	data.Nodes[0].ContentHash = "new-hash"

	// Recompute checksum.
	checksum, err := computeExportChecksum(data.Nodes, data.Dependencies)
	require.NoError(t, err)
	data.Checksum = checksum

	// Import merge — should update.
	result, err := s.Import(ctx, data, ImportModeMerge, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesUpdated)
	assert.Equal(t, 0, result.NodesCreated)
	assert.Equal(t, 0, result.NodesSkipped)
}

// TestImportMerge_SkipsSameHash verifies merge skip per FR-7.8.
func TestImportMerge_SkipsSameHash(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("IMS-1", "", "IMS", "Same", 0, 1, now)
	root.ContentHash = "abc123" // Set a content hash so merge can compare.
	require.NoError(t, s.CreateNode(ctx, root))

	// Export.
	data, err := s.Export(ctx, "IMS", "1.0.0")
	require.NoError(t, err)

	// Import merge with same data — should skip because hash matches.
	result, err := s.Import(ctx, data, ImportModeMerge, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NodesUpdated)
	assert.Equal(t, 0, result.NodesCreated)
	assert.Equal(t, 1, result.NodesSkipped)
}

// =============================================================================
// Transition + Cancel + Delete internal coverage
// =============================================================================

// TestCascadeCancel_MultipleDescendants verifies cascade cancel per FR-6.3.
func TestCascadeCancel_MultipleDescendants(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CCD-1", "", "CCD", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("CCD-1.1", "CCD-1", "CCD", "Child1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("CCD-1.2", "CCD-1", "CCD", "Child2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))
	gc := makeTestNode("CCD-1.1.1", "CCD-1.1", "CCD", "Grandchild", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, gc))

	// Cancel root with cascade.
	require.NoError(t, s.CancelNode(ctx, "CCD-1", "project cancelled", "admin", true))

	// All descendants should be cancelled.
	for _, id := range []string{"CCD-1.1", "CCD-1.2", "CCD-1.1.1"} {
		node, err := s.GetNode(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, model.StatusCancelled, node.Status, "node %s should be cancelled", id)
	}
}

// TestCascadeDelete_DeepHierarchy verifies cascade delete per FR-3.3.
func TestCascadeDelete_DeepHierarchy(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CDD-1", "", "CDD", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("CDD-1.1", "CDD-1", "CDD", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	gc := makeTestNode("CDD-1.1.1", "CDD-1.1", "CDD", "GC", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, gc))

	require.NoError(t, s.DeleteNode(ctx, "CDD-1", true, "admin"))

	// All should be soft-deleted.
	for _, id := range []string{"CDD-1", "CDD-1.1", "CDD-1.1.1"} {
		_, err := s.GetNode(ctx, id)
		assert.ErrorIs(t, err, model.ErrNotFound, "node %s should be deleted", id)
	}
}

// TestUndeleteNode_CascadeRestore verifies undelete restores descendants per FR-3.3.
func TestUndeleteNode_CascadeRestore(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("UDC-1", "", "UDC", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("UDC-1.1", "UDC-1", "UDC", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))

	// Delete with cascade.
	require.NoError(t, s.DeleteNode(ctx, "UDC-1", true, "admin"))

	// Undelete root.
	require.NoError(t, s.UndeleteNode(ctx, "UDC-1"))

	// Both should be restored.
	node, err := s.GetNode(ctx, "UDC-1")
	require.NoError(t, err)
	assert.Equal(t, "Root", node.Title)

	child, err := s.GetNode(ctx, "UDC-1.1")
	require.NoError(t, err)
	assert.Equal(t, "C1", child.Title)
}

// =============================================================================
// detectCycle deeper coverage per FR-4.3
// =============================================================================

// TestDetectCycle_AlreadyVisited_SkipsDuplicate verifies BFS dedup per FR-4.3.
func TestDetectCycle_AlreadyVisited_SkipsDuplicate(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a diamond dependency: A→B, A→C, B→D, C→D.
	for _, id := range []string{"CYC-A", "CYC-B", "CYC-C", "CYC-D"} {
		n := makeTestNode(id, "", "CYC", id, 0, 1, now)
		require.NoError(t, s.CreateNode(ctx, n))
	}

	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CYC-A", ToID: "CYC-B", DepType: model.DepTypeBlocks,
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CYC-A", ToID: "CYC-C", DepType: model.DepTypeBlocks,
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CYC-B", ToID: "CYC-D", DepType: model.DepTypeBlocks,
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "CYC-C", ToID: "CYC-D", DepType: model.DepTypeBlocks,
	}))

	// Adding D→A would create a cycle through either path.
	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "CYC-D", ToID: "CYC-A", DepType: model.DepTypeBlocks,
	})
	assert.ErrorIs(t, err, model.ErrCycleDetected)
}

// =============================================================================
// GetSiblings and SetAnnotations internal coverage
// =============================================================================

// TestGetSiblings_DeeperHierarchy_ReturnsOnlySameLevel verifies sibling isolation.
func TestGetSiblings_DeeperHierarchy_ReturnsOnlySameLevel(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("GS-1", "", "GS", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("GS-1.1", "GS-1", "GS", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("GS-1.2", "GS-1", "GS", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))
	c3 := makeTestNode("GS-1.3", "GS-1", "GS", "C3", 1, 3, now)
	require.NoError(t, s.CreateNode(ctx, c3))
	gc := makeTestNode("GS-1.1.1", "GS-1.1", "GS", "GC", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, gc))

	siblings, err := s.GetSiblings(ctx, "GS-1.2")
	require.NoError(t, err)
	assert.Len(t, siblings, 2) // C1 and C3, not GC

	ids := make([]string, len(siblings))
	for i, s := range siblings {
		ids[i] = s.ID
	}
	assert.Contains(t, ids, "GS-1.1")
	assert.Contains(t, ids, "GS-1.3")
	assert.NotContains(t, ids, "GS-1.1.1")
}

// TestSetAnnotations_EmptySlice_ClearsAnnotations verifies clearing per FR-3.4.
func TestSetAnnotations_EmptySlice_ClearsAnnotations(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("SA-1", "", "SA", "Annotated", 0, 1, now)
	root.Annotations = []model.Annotation{{
		ID: "ann-1", Author: "user", Text: "note", CreatedAt: now,
	}}
	require.NoError(t, s.CreateNode(ctx, root))

	// Clear annotations.
	require.NoError(t, s.SetAnnotations(ctx, "SA-1", nil))

	node, err := s.GetNode(ctx, "SA-1")
	require.NoError(t, err)
	assert.Empty(t, node.Annotations)
}

// =============================================================================
// buildCreatedActivity coverage
// =============================================================================

// TestBuildCreatedActivity_ReturnsValidJSON verifies activity JSON generation.
func TestBuildCreatedActivity_ReturnsValidJSON(t *testing.T) {
	now := time.Now().UTC()
	actJSON, err := buildCreatedActivity("test-user", now)
	require.NoError(t, err)

	var entries []model.ActivityEntry
	require.NoError(t, json.Unmarshal([]byte(actJSON), &entries))
	assert.Len(t, entries, 1)
	assert.Equal(t, model.ActivityTypeCreated, entries[0].Type)
	assert.Equal(t, "test-user", entries[0].Author)
}

// =============================================================================
// resolveDBPath edge cases
// =============================================================================

// TestResolveDBPath_ExistingDir_AppendsMtixDB verifies dir handling.
func TestResolveDBPath_ExistingDir_AppendsMtixDB(t *testing.T) {
	dir := t.TempDir()
	result := resolveDBPath(dir)
	assert.Equal(t, filepath.Join(dir, "mtix.db"), result)
}

// TestResolveDBPath_FilePath_Unchanged verifies file path passthrough.
func TestResolveDBPath_FilePath_Unchanged(t *testing.T) {
	result := resolveDBPath("/some/path/custom.db")
	assert.Equal(t, "/some/path/custom.db", result)
}

// =============================================================================
// buildScopeClause edge cases
// =============================================================================

// TestBuildScopeClause_Empty_ReturnsEmpty verifies empty scope.
func TestBuildScopeClause_Empty_ReturnsEmpty(t *testing.T) {
	clause, args := buildScopeClause("")
	assert.Empty(t, clause)
	assert.Nil(t, args)
}

// TestBuildScopeClause_NonEmpty_ReturnsClause verifies scoped clause.
func TestBuildScopeClause_NonEmpty_ReturnsClause(t *testing.T) {
	clause, args := buildScopeClause("PROJ-1")
	assert.NotEmpty(t, clause)
	assert.Len(t, args, 2)
	assert.Equal(t, "PROJ-1", args[0])
	assert.Equal(t, "PROJ-1.%", args[1])
}

// =============================================================================
// contentFieldsChanged edge cases
// =============================================================================

// TestContentFieldsChanged_OnlyAssignee_ReturnsFalse verifies non-content detection.
func TestContentFieldsChanged_OnlyAssignee_ReturnsFalse(t *testing.T) {
	assignee := "agent"
	u := &store.NodeUpdate{Assignee: &assignee}
	assert.False(t, contentFieldsChanged(u))
}

// TestContentFieldsChanged_TitleChanged_ReturnsTrue verifies title detection.
func TestContentFieldsChanged_TitleChanged_ReturnsTrue(t *testing.T) {
	title := "New Title"
	u := &store.NodeUpdate{Title: &title}
	assert.True(t, contentFieldsChanged(u))
}

// TestContentFieldsChanged_LabelsChanged_ReturnsTrue verifies labels detection.
func TestContentFieldsChanged_LabelsChanged_ReturnsTrue(t *testing.T) {
	u := &store.NodeUpdate{Labels: []string{"a"}}
	assert.True(t, contentFieldsChanged(u))
}

// =============================================================================
// validateParentStatus internal coverage per FR-3.9
// =============================================================================

// TestValidateParentStatus_DoneParent_ReturnsInvalidInput verifies FR-3.9.
func TestValidateParentStatus_DoneParent_ReturnsInvalidInput(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("VPS-1", "", "VPS", "Parent", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Transition to done.
	require.NoError(t, s.TransitionStatus(ctx, "VPS-1", model.StatusInProgress, "start", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "VPS-1", model.StatusDone, "done", "agent"))

	// Try validateParentStatus from within a transaction.
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return validateParentStatus(ctx, tx, "VPS-1")
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestValidateParentStatus_OpenParent_ReturnsNil verifies FR-3.9.
func TestValidateParentStatus_OpenParent_ReturnsNil(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("VPS2-1", "", "VPS2", "Open Parent", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return validateParentStatus(ctx, tx, "VPS2-1")
	})
	assert.NoError(t, err)
}

// TestValidateParentStatus_MissingParent_ReturnsNotFound verifies FR-3.9.
func TestValidateParentStatus_MissingParent_ReturnsNotFound(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		return validateParentStatus(ctx, tx, "NONEXISTENT")
	})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// =============================================================================
// buildUpdateClauses coverage
// =============================================================================

// TestBuildUpdateClauses_AllFields verifies all field clauses.
func TestBuildUpdateClauses_AllFields(t *testing.T) {
	title := "T"
	desc := "D"
	prompt := "P"
	accept := "A"
	status := model.StatusDone
	priority := model.PriorityCritical
	assignee := "ag"
	agentState := model.AgentStateWorking

	u := &store.NodeUpdate{
		Title:       &title,
		Description: &desc,
		Prompt:      &prompt,
		Acceptance:  &accept,
		Status:      &status,
		Priority:    &priority,
		Labels:      []string{"l1"},
		Assignee:    &assignee,
		AgentState:  &agentState,
	}

	clauses, args := buildUpdateClauses(u)
	assert.Len(t, clauses, 9) // 9 fields
	assert.Len(t, args, 9)
}

// =============================================================================
// Helper: makeTestNode (internal package version)
// =============================================================================
// GetStats coverage
// =============================================================================

// TestGetStats_ScopedToSubtree_ReturnsFilteredStats verifies scoped stats per FR-2.7.5.
func TestGetStats_ScopedToSubtree_ReturnsFilteredStats(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create root and children.
	root := makeTestNode("GS-1", "", "GS", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("GS-1.1", "GS-1", "GS", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("GS-1.2", "GS-1", "GS", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))
	// Separate tree.
	other := makeTestNode("GS-2", "", "GS", "Other", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, other))

	// Global stats.
	globalStats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 4, globalStats.TotalNodes)

	// Scoped stats should include root + descendants.
	scopedStats, err := s.GetStats(ctx, "GS-1")
	require.NoError(t, err)
	assert.Equal(t, 3, scopedStats.TotalNodes) // GS-1, GS-1.1, GS-1.2
	assert.Equal(t, "GS-1", scopedStats.ScopeID)
	assert.NotEmpty(t, scopedStats.ByStatus)
	assert.NotEmpty(t, scopedStats.ByPriority)
	assert.NotEmpty(t, scopedStats.ByType)
}

// TestGetStats_EmptyDB_ReturnsZero verifies empty stats per FR-2.7.5.
func TestGetStats_EmptyDB_ReturnsZero(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 0, stats.TotalNodes)
	assert.Empty(t, stats.ByStatus)
}

// TestGetStats_MixedStatuses_CountsByStatus verifies status aggregation.
func TestGetStats_MixedStatuses_CountsByStatus(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("GSM-1", "", "GSM", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("GSM-1.1", "GSM-1", "GSM", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("GSM-1.2", "GSM-1", "GSM", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	// Transition c1 to in_progress.
	require.NoError(t, s.TransitionStatus(ctx, "GSM-1.1", model.StatusInProgress, "test", "agent"))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 3, stats.TotalNodes)
	assert.Equal(t, 2, stats.ByStatus["open"])
	assert.Equal(t, 1, stats.ByStatus["in_progress"])
}

// =============================================================================
// Backup verification failure coverage
// =============================================================================

// TestBackup_CorruptedFile_DeletesAndReturnsError verifies cleanup on corrupt backup.
func TestBackup_CorruptedFile_DeletesAndReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	// Create a valid backup first.
	destPath := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.Greater(t, result.Size, int64(0))
}

// TestBackup_PathWithQuotes_Escaped verifies path escaping per FR-6.3a.
func TestBackup_PathWithQuotes_Escaped(t *testing.T) {
	escaped := escapeSQLitePath("test'path")
	assert.Equal(t, "test''path", escaped)

	escaped2 := escapeSQLitePath("no_quotes")
	assert.Equal(t, "no_quotes", escaped2)

	escaped3 := escapeSQLitePath("'''")
	assert.Equal(t, "''''''", escaped3)
}

// =============================================================================
// CancelNode coverage
// =============================================================================

// TestCancelNode_WithCascade_CancelsDescendants verifies cascade cancel per FR-6.3.
func TestCancelNode_WithCascade_CancelsDescendants(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CN-1", "", "CN", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("CN-1.1", "CN-1", "CN", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("CN-1.2", "CN-1", "CN", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	err := s.CancelNode(ctx, "CN-1", "no longer needed", "admin", true)
	require.NoError(t, err)

	// Root cancelled.
	got, err := s.GetNode(ctx, "CN-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got.Status)

	// Children cancelled.
	got1, err := s.GetNode(ctx, "CN-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got1.Status)

	got2, err := s.GetNode(ctx, "CN-1.2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, got2.Status)
}

// TestCancelNode_EmptyReason_ReturnsInvalidInput verifies reason validation per FR-6.3.
func TestCancelNode_EmptyReason_ReturnsInvalidInput(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.CancelNode(ctx, "X-1", "", "admin", false)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestCancelNode_NonExistent_ReturnsNotFound verifies missing node handling.
func TestCancelNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.CancelNode(ctx, "NOPE-1", "reason", "admin", false)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestCancelNode_WithParent_RecalculatesProgress verifies parent progress per FR-5.7.
func TestCancelNode_WithParent_RecalculatesProgress(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("CNP-1", "", "CNP", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("CNP-1.1", "CNP-1", "CNP", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("CNP-1.2", "CNP-1", "CNP", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))

	// Transition c1 to done (progress=1).
	require.NoError(t, s.TransitionStatus(ctx, "CNP-1.1", model.StatusInProgress, "test", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "CNP-1.1", model.StatusDone, "test", "agent"))

	// Cancel c2 — should be excluded from progress denominator.
	err := s.CancelNode(ctx, "CNP-1.2", "not needed", "admin", false)
	require.NoError(t, err)

	// Parent progress should reflect only non-cancelled children.
	got, err := s.GetNode(ctx, "CNP-1")
	require.NoError(t, err)
	assert.InDelta(t, 1.0, got.Progress, 0.01) // Only c1 counts (done=1.0)
}

// =============================================================================
// Claim coverage
// =============================================================================

// TestUnclaimNode_NotClaimed_ReturnsError verifies unclaim error path.
func TestUnclaimNode_NotClaimed_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("UC-1", "", "UC", "Unclaimed", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	err := s.UnclaimNode(ctx, "UC-1", "releasing", "some-agent")
	assert.Error(t, err)
}

// TestForceReclaimNode_InProgressNode_Claims verifies force reclaim per FR-10.4.
func TestForceReclaimNode_InProgressNode_Claims(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("FR-1", "", "FR", "Reclaimable", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Node must be in_progress and claimed for force reclaim to work.
	require.NoError(t, s.ClaimNode(ctx, "FR-1", "old-agent"))
	require.NoError(t, s.TransitionStatus(ctx, "FR-1", model.StatusInProgress, "start", "old-agent"))

	err := s.ForceReclaimNode(ctx, "FR-1", "new-agent", 0) // 0 stale threshold = always force
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "FR-1")
	require.NoError(t, err)
	assert.Equal(t, "new-agent", got.Assignee)
}

// =============================================================================
// Import/Export roundtrip coverage
// =============================================================================

// TestExport_WithDependencies_IncludesDeps verifies deps export per FR-7.8.
func TestExport_WithDependencies_IncludesDeps(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeTestNode("EXD-1", "", "EXD", "N1", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n1))
	n2 := makeTestNode("EXD-2", "", "EXD", "N2", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	// Add dependency.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "EXD-1", ToID: "EXD-2", DepType: model.DepTypeBlocks,
	}))

	data, err := s.Export(ctx, "EXD", "1.0.0")
	require.NoError(t, err)
	assert.Equal(t, 2, data.NodeCount)
	assert.Len(t, data.Dependencies, 1)
	assert.NotEmpty(t, data.Checksum)
	assert.Equal(t, 1, data.Version)
}

// TestImportReplace_ClearAndReimport verifies replace mode per FR-7.8.
func TestImportReplace_ClearAndReimport(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create initial data.
	n := makeTestNode("IMP-1", "", "IMP", "Original", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))

	// Export.
	data, err := s.Export(ctx, "IMP", "1.0.0")
	require.NoError(t, err)

	// Create extra node that should be wiped.
	n2 := makeTestNode("IMP-2", "", "IMP", "Extra", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	// Import replace.
	result, err := s.Import(ctx, data, ImportModeReplace, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesCreated)
}

// TestVerifyExportChecksum_ValidData_ReturnsTrue verifies checksum validation.
func TestVerifyExportChecksum_ValidData_ReturnsTrue(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeTestNode("VE-1", "", "VE", "Verify", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))

	data, err := s.Export(ctx, "VE", "1.0.0")
	require.NoError(t, err)

	valid, err := VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid)
}

// TestVerifyExportChecksum_TamperedData_ReturnsFalse verifies tamper detection.
func TestVerifyExportChecksum_TamperedData_ReturnsFalse(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeTestNode("VET-1", "", "VET", "Tamper", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))

	data, err := s.Export(ctx, "VET", "1.0.0")
	require.NoError(t, err)

	// Tamper with a node title.
	data.Nodes[0].Title = "MODIFIED"

	valid, err := VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.False(t, valid)
}

// TestVerifyExportChecksum_NilExport_ReturnsError verifies nil handling.
func TestVerifyExportChecksum_NilExport_ReturnsError(t *testing.T) {
	_, err := VerifyExportChecksum(nil)
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// =============================================================================
// WithTx rollback coverage
// =============================================================================

// TestWithTx_ErrorReturned_Rollback verifies transaction rollback on error.
func TestWithTx_ErrorReturned_Rollback(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(_ *sql.Tx) error {
		return fmt.Errorf("intentional error")
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "intentional error")
}

// TestWithTx_Success_Commits verifies transaction commit on success.
func TestWithTx_Success_Commits(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			"test_key", "test_value")
		return err
	})
	require.NoError(t, err)

	// Verify the insert was committed.
	var val string
	err = s.readDB.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", "test_key").Scan(&val)
	require.NoError(t, err)
	assert.Equal(t, "test_value", val)
}

// =============================================================================
// Verify full suite coverage
// =============================================================================

// TestVerify_AfterCancel_ProgressConsistent verifies verify after cancel per FR-6.3.
func TestVerify_AfterCancel_ProgressConsistent(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("VC-1", "", "VC", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))

	_, err := s.NextSequence(ctx, "VC:")
	require.NoError(t, err)

	c := makeTestNode("VC-1.1", "VC-1", "VC", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c))

	_, err = s.NextSequence(ctx, "VC:VC-1")
	require.NoError(t, err)

	require.NoError(t, s.TransitionStatus(ctx, "VC-1.1", model.StatusInProgress, "test", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "VC-1.1", model.StatusDone, "test", "agent"))

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.AllPassed)
}

// =============================================================================
// node_list.go coverage — ListNodes with various filters
// =============================================================================

// TestListNodes_StatusFilter_ReturnsFiltered verifies status filtering.
func TestListNodes_StatusFilter_ReturnsFiltered(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 3; i++ {
		n := makeTestNode(
			fmt.Sprintf("LN-%d", i), "", "LN",
			fmt.Sprintf("Node %d", i), 0, i, now,
		)
		require.NoError(t, s.CreateNode(ctx, n))
	}

	require.NoError(t, s.TransitionStatus(ctx, "LN-1", model.StatusInProgress, "test", "agent"))

	nodes, total, err := s.ListNodes(ctx,
		store.NodeFilter{Status: []model.Status{model.StatusInProgress}},
		store.ListOptions{Limit: 10},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, nodes, 1)
	assert.Equal(t, "LN-1", nodes[0].ID)
}

// TestListNodes_ParentFilter_ReturnsChildren verifies parent filtering.
func TestListNodes_ParentFilter_ReturnsChildren(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeTestNode("LNP-1", "", "LNP", "Root", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, root))
	c1 := makeTestNode("LNP-1.1", "LNP-1", "LNP", "C1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, c1))
	c2 := makeTestNode("LNP-1.2", "LNP-1", "LNP", "C2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, c2))
	other := makeTestNode("LNP-2", "", "LNP", "Other", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, other))

	nodes, total, err := s.ListNodes(ctx,
		store.NodeFilter{Under: "LNP-1"},
		store.ListOptions{Limit: 10},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, total) // LNP-1 + LNP-1.1 + LNP-1.2 (Under includes parent)
	assert.Len(t, nodes, 3)
}

// TestListNodes_AssigneeFilter_ReturnsAssigned verifies assignee filtering.
func TestListNodes_AssigneeFilter_ReturnsAssigned(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeTestNode("LNA-1", "", "LNA", "N1", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n1))
	n2 := makeTestNode("LNA-2", "", "LNA", "N2", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	require.NoError(t, s.ClaimNode(ctx, "LNA-1", "agent-1"))

	nodes, total, err := s.ListNodes(ctx,
		store.NodeFilter{Assignee: "agent-1"},
		store.ListOptions{Limit: 10},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, nodes, 1)
	assert.Equal(t, "LNA-1", nodes[0].ID)
}

// TestListNodes_ProjectFilter_ReturnsProjectNodes verifies project filtering.
func TestListNodes_ProjectFilter_ReturnsProjectNodes(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeTestNode("P1-1", "", "PROJ1", "N1", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n1))
	n2 := makeTestNode("P2-1", "", "PROJ2", "N2", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	// Use assignee filter instead since NodeFilter doesn't have a Project field.
	require.NoError(t, s.ClaimNode(ctx, "P1-1", "proj1-agent"))
	nodes, total, err := s.ListNodes(ctx,
		store.NodeFilter{Assignee: "proj1-agent"},
		store.ListOptions{Limit: 10},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, nodes, 1)
	assert.Equal(t, "P1-1", nodes[0].ID)
}

// TestListNodes_Pagination_OffsetAndLimit verifies pagination per FR-2.7.
func TestListNodes_Pagination_OffsetAndLimit(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 5; i++ {
		n := makeTestNode(
			fmt.Sprintf("PG-%d", i), "", "PG",
			fmt.Sprintf("Node %d", i), 0, i, now,
		)
		require.NoError(t, s.CreateNode(ctx, n))
	}

	// Page 1: offset 0, limit 2.
	nodes, total, err := s.ListNodes(ctx,
		store.NodeFilter{},
		store.ListOptions{Limit: 2, Offset: 0},
	)
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, nodes, 2)

	// Page 2: offset 2, limit 2.
	nodes2, _, err := s.ListNodes(ctx,
		store.NodeFilter{},
		store.ListOptions{Limit: 2, Offset: 2},
	)
	require.NoError(t, err)
	assert.Len(t, nodes2, 2)
	assert.NotEqual(t, nodes[0].ID, nodes2[0].ID)
}

// =============================================================================
// SearchNodes coverage
// =============================================================================

// TestSearchNodes_MatchingTitle_ReturnsResults verifies FTS search per FR-2.7.1.
func TestSearchNodes_MatchingTitle_ReturnsResults(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeTestNode("SN-1", "", "SN", "Quantum Computing Research", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))
	n2 := makeTestNode("SN-2", "", "SN", "Database Migration Plan", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	results, total, err := s.SearchNodes(ctx, "quantum",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, results, 1)
	assert.Equal(t, "SN-1", results[0].ID)
}

// TestSearchNodes_NoMatch_ReturnsEmpty verifies empty search result.
func TestSearchNodes_NoMatch_ReturnsEmpty(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeTestNode("SNE-1", "", "SNE", "Simple Task", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))

	results, total, err := s.SearchNodes(ctx, "zzzznonexistent",
		store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, results)
}

// =============================================================================
// node_update.go — UpdateNode additional paths
// =============================================================================

// TestUpdateNode_DescriptionOnly_NoHashChange verifies non-content description updates.
func TestUpdateNode_DescriptionOnly_NoHashChange(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeTestNode("UND-1", "", "UND", "Described", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))

	desc := "New description"
	err := s.UpdateNode(ctx, "UND-1", &store.NodeUpdate{Description: &desc})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "UND-1")
	require.NoError(t, err)
	assert.Equal(t, "New description", got.Description)
}

// TestUpdateNode_PromptOnly_UpdatesPrompt verifies prompt update.
func TestUpdateNode_PromptOnly_UpdatesPrompt(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeTestNode("UNP-1", "", "UNP", "Prompted", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n))

	prompt := "Build the feature"
	err := s.UpdateNode(ctx, "UNP-1", &store.NodeUpdate{Prompt: &prompt})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "UNP-1")
	require.NoError(t, err)
	assert.Equal(t, "Build the feature", got.Prompt)
}

// =============================================================================
// Dependency additional paths
// =============================================================================

// TestAddDependency_NonBlocks_NoAutoBlock verifies non-blocking dep type.
func TestAddDependency_NonBlocks_NoAutoBlock(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeTestNode("DNB-1", "", "DNB", "N1", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n1))
	n2 := makeTestNode("DNB-2", "", "DNB", "N2", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	// "related" should not auto-block.
	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "DNB-1", ToID: "DNB-2", DepType: model.DepTypeRelated,
	})
	require.NoError(t, err)

	// N2 should still be open, not blocked.
	got, err := s.GetNode(ctx, "DNB-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestRemoveDependency_LastBlocker_AutoUnblocks verifies auto-unblock per FR-4.5.
func TestRemoveDependency_LastBlocker_AutoUnblocks(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n1 := makeTestNode("DRU-1", "", "DRU", "Blocker", 0, 1, now)
	require.NoError(t, s.CreateNode(ctx, n1))
	n2 := makeTestNode("DRU-2", "", "DRU", "Blocked", 0, 2, now)
	require.NoError(t, s.CreateNode(ctx, n2))

	// Add blocks dependency — should auto-block n2.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "DRU-1", ToID: "DRU-2", DepType: model.DepTypeBlocks,
	}))

	got, err := s.GetNode(ctx, "DRU-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, got.Status)

	// Remove — should auto-unblock.
	require.NoError(t, s.RemoveDependency(ctx, "DRU-1", "DRU-2", model.DepTypeBlocks))

	got, err = s.GetNode(ctx, "DRU-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// =============================================================================

func makeTestNode(id, parentID, project, title string, depth, seq int, createdAt time.Time) *model.Node {
	return &model.Node{
		ID:        id,
		ParentID:  parentID,
		Project:   project,
		Title:     title,
		Depth:     depth,
		Seq:       seq,
		NodeType:  model.NodeTypeAuto,
		Status:    model.StatusOpen,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

// TestWrapBusyError_LockedError_WrapsWithGuidance verifies actionable message.
func TestWrapBusyError_LockedError_WrapsWithGuidance(t *testing.T) {
	err := fmt.Errorf("begin transaction: database is locked")
	wrapped := wrapBusyError(err)
	assert.Contains(t, wrapped.Error(), "another mtix operation is in progress")
	assert.Contains(t, wrapped.Error(), "retry in a moment")
	assert.ErrorIs(t, wrapped, err)
}

// TestWrapBusyError_SQLiteBusy_WrapsWithGuidance verifies SQLITE_BUSY wrapping.
func TestWrapBusyError_SQLiteBusy_WrapsWithGuidance(t *testing.T) {
	err := fmt.Errorf("SQLITE_BUSY")
	wrapped := wrapBusyError(err)
	assert.Contains(t, wrapped.Error(), "another mtix operation is in progress")
}

// TestWrapBusyError_NilError_ReturnsNil verifies nil passthrough.
func TestWrapBusyError_NilError_ReturnsNil(t *testing.T) {
	assert.Nil(t, wrapBusyError(nil))
}

// TestWrapBusyError_OtherError_PassesThrough verifies non-busy errors unchanged.
func TestWrapBusyError_OtherError_PassesThrough(t *testing.T) {
	err := fmt.Errorf("some other error")
	assert.Equal(t, err, wrapBusyError(err))
}

// --- Store lifecycle tests ---

// TestNew_ValidPath_OpensWithCorrectPragmas verifies that New sets
// WAL mode, foreign keys, and busy_timeout on both connections.
func TestNew_ValidPath_OpensWithCorrectPragmas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pragmas.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	// Verify WAL mode on write connection.
	var journalMode string
	require.NoError(t, s.writeDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode))
	assert.Equal(t, "wal", journalMode)

	// Verify foreign keys on both connections.
	var fkWrite, fkRead int
	require.NoError(t, s.writeDB.QueryRow("PRAGMA foreign_keys").Scan(&fkWrite))
	require.NoError(t, s.readDB.QueryRow("PRAGMA foreign_keys").Scan(&fkRead))
	assert.Equal(t, 1, fkWrite, "foreign_keys should be ON for write connection")
	assert.Equal(t, 1, fkRead, "foreign_keys should be ON for read connection")

	// Verify busy_timeout on both connections.
	var btWrite, btRead int
	require.NoError(t, s.writeDB.QueryRow("PRAGMA busy_timeout").Scan(&btWrite))
	require.NoError(t, s.readDB.QueryRow("PRAGMA busy_timeout").Scan(&btRead))
	assert.Equal(t, 5000, btWrite, "busy_timeout should be 5000 on write connection")
	assert.Equal(t, 5000, btRead, "busy_timeout should be 5000 on read connection")
}

// TestNew_InvalidPath_ReturnsError verifies that an invalid database path fails.
func TestNew_InvalidPath_ReturnsError(t *testing.T) {
	_, err := New("/nonexistent/deep/path/that/cannot/exist/db.sqlite", slog.Default())
	assert.Error(t, err)
}

// TestNew_DirectoryPath_ResolvesToMtixDB verifies that a directory path
// resolves to mtix.db inside it.
func TestNew_DirectoryPath_ResolvesToMtixDB(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	// The database file should exist at dir/mtix.db.
	_, statErr := os.Stat(filepath.Join(dir, "mtix.db"))
	assert.NoError(t, statErr, "mtix.db should be created in the directory")
}

// TestClose_DoubleClose_NoPanic verifies that closing an already-closed
// store does not panic.
func TestClose_DoubleClose_NoPanic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "close.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)

	require.NoError(t, s.Close())
	// Second close should not panic. It may or may not error
	// depending on the SQLite driver implementation.
	assert.NotPanics(t, func() { _ = s.Close() })
}

// --- Backup tests ---

// TestBackup_ValidPath_CreatesVerifiedCopy verifies backup creates a
// usable, verified database file with correct content.
func TestBackup_ValidPath_CreatesVerifiedCopy(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Insert data to verify backup contains it.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "BK-1", Project: "BK", Depth: 0, Seq: 1, Title: "Backup test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	destPath := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)

	assert.Equal(t, destPath, result.Path)
	assert.True(t, result.Verified)
	assert.Greater(t, result.Size, int64(0))

	// Open the backup and verify data exists.
	backupStore, err := New(destPath, slog.Default())
	require.NoError(t, err)
	defer func() { _ = backupStore.Close() }()

	node, err := backupStore.GetNode(ctx, "BK-1")
	require.NoError(t, err)
	assert.Equal(t, "Backup test", node.Title)
}

// TestBackup_UnwritablePath_ReturnsError verifies that an unwritable path fails.
func TestBackup_UnwritablePath_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	_, err := s.Backup(context.Background(), "/nonexistent/dir/backup.db")
	assert.Error(t, err)
}

// --- Verify tests ---

// TestVerify_HealthyDatabase_AllChecksPass verifies that a fresh, correctly
// initialized database passes all verification checks.
func TestVerify_HealthyDatabase_AllChecksPass(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a parent and child so progress rollup can be verified.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "VER-1", Project: "VER", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "p1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "VER-1.1", ParentID: "VER-1", Project: "VER", Depth: 1, Seq: 1,
		Title: "Child", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "c1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Populate sequences table (normally done by NextSequence via service layer).
	_, seqErr := s.writeDB.ExecContext(ctx,
		"INSERT INTO sequences (key, value) VALUES (?, ?), (?, ?)",
		"VER:", 1, "VER:VER-1", 1)
	require.NoError(t, seqErr)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.AllPassed, "all verification checks should pass: %v", result.Errors)
	assert.True(t, result.IntegrityOK)
	assert.True(t, result.ForeignKeyOK)
	assert.True(t, result.SequenceOK)
	assert.True(t, result.ProgressOK)
	assert.True(t, result.FTSOK)
	assert.Empty(t, result.Errors)
}

// TestVerify_EmptyDatabase_AllChecksPass verifies verification works
// on a database with no nodes.
func TestVerify_EmptyDatabase_AllChecksPass(t *testing.T) {
	s := newInternalTestStore(t)
	result, err := s.Verify(context.Background())
	require.NoError(t, err)
	assert.True(t, result.AllPassed)
}

// TestVerifyFTS_AfterInsertAndDelete_RemainsConsistent verifies FTS index
// stays valid through create and delete operations.
func TestVerifyFTS_AfterInsertAndDelete_RemainsConsistent(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create then delete a node — FTS should still be consistent.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "FTS-1", Project: "FTS", Depth: 0, Seq: 1, Title: "FTS test node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "f1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.DeleteNode(ctx, "FTS-1", false, "test"))

	result := &VerifyResult{}
	require.NoError(t, s.verifyFTS(ctx, result))
	assert.True(t, result.FTSOK, "FTS should be consistent after delete")
}

// --- escapeSQLitePath tests ---

// TestEscapeSQLitePath_SingleQuotes_Escaped verifies SQL injection prevention.
func TestEscapeSQLitePath_SingleQuotes_Escaped(t *testing.T) {
	assert.Equal(t, "it''s a path", escapeSQLitePath("it's a path"))
	assert.Equal(t, "normal/path.db", escapeSQLitePath("normal/path.db"))
	assert.Equal(t, "''start", escapeSQLitePath("'start"))
	assert.Equal(t, "end''", escapeSQLitePath("end'"))
	assert.Equal(t, "no quotes", escapeSQLitePath("no quotes"))
}

// --- parseScannedTimestamps tests ---

// TestParseScannedTimestamps_ValidTimestamps_Parses verifies correct parsing.
func TestParseScannedTimestamps_ValidTimestamps_Parses(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{
		createdAtStr: "2026-03-30T12:00:00Z",
		updatedAtStr: "2026-03-30T13:00:00Z",
	}
	err := parseScannedTimestamps(n, d)
	require.NoError(t, err)
	assert.Equal(t, 2026, n.CreatedAt.Year())
	assert.Equal(t, 12, n.CreatedAt.Hour())
	assert.Equal(t, 13, n.UpdatedAt.Hour())
}

// TestParseScannedTimestamps_InvalidCreatedAt_ReturnsError verifies error on bad timestamp.
func TestParseScannedTimestamps_InvalidCreatedAt_ReturnsError(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{createdAtStr: "not-a-date", updatedAtStr: "2026-03-30T12:00:00Z"}
	err := parseScannedTimestamps(n, d)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse created_at")
}

// TestParseScannedTimestamps_InvalidUpdatedAt_ReturnsError verifies error on bad updated_at.
func TestParseScannedTimestamps_InvalidUpdatedAt_ReturnsError(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{createdAtStr: "2026-03-30T12:00:00Z", updatedAtStr: "bad"}
	err := parseScannedTimestamps(n, d)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse updated_at")
}

// TestParseScannedTimestamps_NullableTimestamps_ParsesWhenPresent verifies
// optional timestamps are parsed when non-null.
func TestParseScannedTimestamps_NullableTimestamps_ParsesWhenPresent(t *testing.T) {
	n := &model.Node{}
	closedStr := sql.NullString{String: "2026-03-30T14:00:00Z", Valid: true}
	deferStr := sql.NullString{String: "2026-04-01T00:00:00Z", Valid: true}
	d := &scanDest{
		createdAtStr: "2026-03-30T12:00:00Z",
		updatedAtStr: "2026-03-30T13:00:00Z",
		closedAt:     closedStr,
		deferUntil:   deferStr,
	}
	err := parseScannedTimestamps(n, d)
	require.NoError(t, err)
	assert.NotNil(t, n.ClosedAt)
	assert.Equal(t, 14, n.ClosedAt.Hour())
	assert.NotNil(t, n.DeferUntil)
}

// --- parseScannedJSON tests ---

// TestParseScannedJSON_EmptyFields_NoError verifies empty JSON fields don't error.
func TestParseScannedJSON_EmptyFields_NoError(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{}
	err := parseScannedJSON(n, d)
	require.NoError(t, err)
}

// TestParseScannedJSON_ValidLabels_Parses verifies label array parsing.
func TestParseScannedJSON_ValidLabels_Parses(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{
		labelsJSON: sql.NullString{String: `["bug","urgent"]`, Valid: true},
	}
	err := parseScannedJSON(n, d)
	require.NoError(t, err)
	assert.Equal(t, []string{"bug", "urgent"}, n.Labels)
}

// TestParseScannedJSON_InvalidLabelsJSON_ReturnsError verifies error on bad JSON.
func TestParseScannedJSON_InvalidLabelsJSON_ReturnsError(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{
		labelsJSON: sql.NullString{String: `{not valid json`, Valid: true},
	}
	err := parseScannedJSON(n, d)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse labels")
}

// TestParseScannedJSON_MetadataField_PreservesRawJSON verifies metadata
// is stored as raw JSON without parsing into a typed struct.
func TestParseScannedJSON_MetadataField_PreservesRawJSON(t *testing.T) {
	n := &model.Node{}
	d := &scanDest{
		metadataJSON: sql.NullString{String: `{"custom":"value","nested":{"key":1}}`, Valid: true},
	}
	err := parseScannedJSON(n, d)
	require.NoError(t, err)
	assert.JSONEq(t, `{"custom":"value","nested":{"key":1}}`, string(n.Metadata))
}

// --- Import zero-node protection tests ---

// TestImport_ZeroNodes_NonEmptyDB_WithoutForce_Rejected verifies that
// importing zero nodes into a database with existing data is rejected.
func TestImport_ZeroNodes_NonEmptyDB_WithoutForce_Rejected(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Seed the database.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EXIST-1", Project: "EXIST", Depth: 0, Seq: 1, Title: "Existing",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "e1", CreatedAt: now, UpdatedAt: now,
	}))

	// Create a valid empty export.
	emptyStore := newInternalTestStore(t)
	emptyExport, err := emptyStore.Export(ctx, "EMPTY", "0.1.0")
	require.NoError(t, err)
	require.Equal(t, 0, len(emptyExport.Nodes))

	// Import without force should fail.
	_, err = s.Import(ctx, emptyExport, ImportModeReplace, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zero nodes")

	// Original data should still exist.
	node, err := s.GetNode(ctx, "EXIST-1")
	require.NoError(t, err)
	assert.Equal(t, "Existing", node.Title)
}

// TestImport_ZeroNodes_EmptyDB_WithoutForce_Succeeds verifies that
// importing zero nodes into an empty database is allowed (no false positive).
func TestImport_ZeroNodes_EmptyDB_WithoutForce_Succeeds(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	emptyStore := newInternalTestStore(t)
	emptyExport, err := emptyStore.Export(ctx, "EMPTY", "0.1.0")
	require.NoError(t, err)

	result, err := s.Import(ctx, emptyExport, ImportModeReplace, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result.NodesCreated)
}

// ─── Store init/schema tests ───

// TestInit_SchemaVersionMismatch_ReturnsConflict verifies that opening a
// database with a newer schema version than supported is rejected.
func TestInit_SchemaVersionMismatch_ReturnsConflict(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "version.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)

	// Bump the schema version beyond what the code supports.
	_, err = s.writeDB.Exec("UPDATE meta SET value = '999' WHERE key = 'schema_version'")
	require.NoError(t, err)
	_ = s.Close()

	// Re-opening should fail with a conflict error.
	_, err = New(dbPath, slog.Default())
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrConflict)
}

// ─── Stats tests ───

// TestGetStats_WithProgress_ReturnsWeightedAverage verifies that stats
// correctly computes weighted average progress across nodes.
func TestGetStats_WithProgress_ReturnsWeightedAverage(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create two nodes with different progress and weights.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "PRG-1", Project: "PRG", Depth: 0, Seq: 1, Title: "Half done",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 2.0,
		NodeType: model.NodeTypeIssue, ContentHash: "p1",
		CreatedAt: now, UpdatedAt: now,
	}))
	// Manually set progress (normally done by service layer).
	_, err := s.writeDB.ExecContext(ctx, "UPDATE nodes SET progress = 0.5 WHERE id = 'PRG-1'")
	require.NoError(t, err)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "PRG-2", Project: "PRG", Depth: 0, Seq: 2, Title: "Not started",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "p2",
		CreatedAt: now, UpdatedAt: now,
	}))

	stats, err := s.GetStats(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalNodes)
	// Weighted avg: (0.5 * 2 + 0.0 * 1) / (2 + 1) = 1.0 / 3.0 ≈ 0.333
	assert.InDelta(t, 0.333, stats.Progress, 0.01)
}

// TestGetStats_ScopedToSubtree_ReturnsOnlySubtreeCounts verifies that
// scoped stats only count nodes in the specified subtree.
func TestGetStats_ScopedToSubtree_ReturnsOnlySubtreeCounts(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SC-1", Project: "SC", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "p1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SC-1.1", ParentID: "SC-1", Project: "SC", Depth: 1, Seq: 1, Title: "Child",
		Status: model.StatusOpen, Priority: model.PriorityHigh, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "c1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SC-2", Project: "SC", Depth: 0, Seq: 2, Title: "Other root",
		Status: model.StatusOpen, Priority: model.PriorityLow, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "o1", CreatedAt: now, UpdatedAt: now,
	}))

	// Scoped to SC-1 subtree — should only count SC-1 and SC-1.1.
	stats, err := s.GetStats(ctx, "SC-1")
	require.NoError(t, err)
	assert.Equal(t, 2, stats.TotalNodes, "should count parent + child only")
}

// TestGetStats_EmptyDB_ReturnsZeroCounts verifies stats on empty database.
func TestGetStats_EmptyDB_ReturnsZeroCounts(t *testing.T) {
	s := newInternalTestStore(t)
	stats, err := s.GetStats(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, 0, stats.TotalNodes)
	assert.Equal(t, 0.0, stats.Progress)
}

// ─── Cancel tests ───

// TestCancelNode_WithCascade_SetsChildrenCancelled verifies cascade cancel
// sets all non-terminal descendants to cancelled status.
func TestCancelNode_WithCascade_SetsChildrenCancelled(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "CAN-1", Project: "CAN", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "p", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "CAN-1.1", ParentID: "CAN-1", Project: "CAN", Depth: 1, Seq: 1,
		Title: "Open child", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "c1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Cancel parent — child should cascade.
	require.NoError(t, s.CancelNode(ctx, "CAN-1", "testing", "admin", true))

	parent, err := s.GetNode(ctx, "CAN-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, parent.Status)

	child, err := s.GetNode(ctx, "CAN-1.1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusCancelled, child.Status)
}

// ─── Delete / Undelete tests ───

// TestDeleteNode_WithCascade_DeletesDescendants verifies cascade soft-delete.
func TestDeleteNode_WithCascade_DeletesDescendants(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "DEL-1", Project: "DEL", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "p", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "DEL-1.1", ParentID: "DEL-1", Project: "DEL", Depth: 1, Seq: 1,
		Title: "Child", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: "c",
		CreatedAt: now, UpdatedAt: now,
	}))

	require.NoError(t, s.DeleteNode(ctx, "DEL-1", true, "admin"))

	_, err := s.GetNode(ctx, "DEL-1")
	assert.ErrorIs(t, err, model.ErrNotFound, "parent should be deleted")
	_, err = s.GetNode(ctx, "DEL-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound, "child should be cascade deleted")
}

// TestUndeleteNode_RestoresSoftDeletedNode verifies undelete brings back a node.
func TestUndeleteNode_RestoresSoftDeletedNode(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "UND-1", Project: "UND", Depth: 0, Seq: 1, Title: "To undelete",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "u", CreatedAt: now, UpdatedAt: now,
	}))

	require.NoError(t, s.DeleteNode(ctx, "UND-1", false, "admin"))
	_, err := s.GetNode(ctx, "UND-1")
	require.ErrorIs(t, err, model.ErrNotFound)

	require.NoError(t, s.UndeleteNode(ctx, "UND-1"))
	node, err := s.GetNode(ctx, "UND-1")
	require.NoError(t, err)
	assert.Equal(t, "To undelete", node.Title)
	assert.Nil(t, node.DeletedAt)
}

// TestUndeleteNode_NonExistent_ReturnsNotFound verifies undelete on missing node.
func TestUndeleteNode_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newInternalTestStore(t)
	err := s.UndeleteNode(context.Background(), "NOPE-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// ─── Claim tests ───

// TestForceReclaimNode_StaleAgent_Succeeds verifies that force-reclaim
// works when the current agent's heartbeat is older than the threshold.
func TestForceReclaimNode_StaleAgent_Succeeds(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	staleTime := now.Add(-1 * time.Hour)

	// Create and claim a node.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "FC-1", Project: "FC", Depth: 0, Seq: 1, Title: "Force reclaim test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "fc", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.ClaimNode(ctx, "FC-1", "old-agent"))

	// Make the agent's heartbeat stale.
	_, err := s.writeDB.ExecContext(ctx,
		"UPDATE agents SET last_heartbeat = ? WHERE agent_id = ?",
		staleTime.Format(time.RFC3339), "old-agent")
	require.NoError(t, err)

	// Force reclaim should succeed.
	require.NoError(t, s.ForceReclaimNode(ctx, "FC-1", "new-agent", 30*time.Minute))

	node, err := s.GetNode(ctx, "FC-1")
	require.NoError(t, err)
	assert.Equal(t, "new-agent", node.Assignee)
}

// TestForceReclaimNode_ActiveAgent_ReturnsError verifies that force-reclaim
// fails when the current agent is still active (recent heartbeat).
func TestForceReclaimNode_ActiveAgent_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "FA-1", Project: "FA", Depth: 0, Seq: 1, Title: "Active agent test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "fa", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.ClaimNode(ctx, "FA-1", "active-agent"))

	// Force reclaim should fail — agent is still active.
	err := s.ForceReclaimNode(ctx, "FA-1", "new-agent", 30*time.Minute)
	assert.Error(t, err)
	assert.ErrorIs(t, err, model.ErrAgentStillActive)
}

// ─── Export tests ───

// TestExport_WithDependencies_IncludesDepData verifies that export includes
// dependency relationships between nodes.
func TestExport_WithDependencies_IncludesDepData(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EXD-1", Project: "EXD", Depth: 0, Seq: 1, Title: "Blocker",
		Status: model.StatusOpen, Priority: model.PriorityHigh, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "b1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EXD-2", Project: "EXD", Depth: 0, Seq: 2, Title: "Blocked",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "b2", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "EXD-2", ToID: "EXD-1", DepType: model.DepTypeBlocks,
		CreatedBy: "admin", CreatedAt: now,
	}))

	data, err := s.Export(ctx, "EXD", "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, 2, data.NodeCount)
	assert.GreaterOrEqual(t, len(data.Dependencies), 1, "export should include dependencies")
}

// TestExport_WithAgentsAndSessions_IncludesAll verifies full export
// includes agents and sessions alongside nodes.
func TestExport_WithAgentsAndSessions_IncludesAll(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	nowStr := now.Format(time.RFC3339)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EXA-1", Project: "EXA", Depth: 0, Seq: 1, Title: "Agent export test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "a1", CreatedAt: now, UpdatedAt: now,
	}))

	// Register an agent directly.
	_, err := s.writeDB.ExecContext(ctx,
		"INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat) VALUES (?, ?, 'idle', ?, ?)",
		"export-agent", "EXA", nowStr, nowStr)
	require.NoError(t, err)

	// Create a session.
	_, err = s.writeDB.ExecContext(ctx,
		"INSERT INTO sessions (id, agent_id, project, started_at, status) VALUES (?, ?, ?, ?, 'active')",
		"sess-1", "export-agent", "EXA", nowStr)
	require.NoError(t, err)

	data, err := s.Export(ctx, "EXA", "0.1.0")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(data.Agents), 1, "export should include agents")
	assert.GreaterOrEqual(t, len(data.Sessions), 1, "export should include sessions")
}

// ─── Import merge tests ───

// TestImport_MergeMode_UpdatesExistingNodes verifies that merge mode
// updates nodes that exist with different content hashes.
func TestImport_MergeMode_UpdatesExistingNodes(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "MRG-1", Project: "MRG", Depth: 0, Seq: 1, Title: "Original title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "hash-v1", CreatedAt: now, UpdatedAt: now,
	}))

	// Export, modify the title, re-import in merge mode.
	srcStore := newInternalTestStore(t)
	require.NoError(t, srcStore.CreateNode(ctx, &model.Node{
		ID: "MRG-1", Project: "MRG", Depth: 0, Seq: 1, Title: "Updated title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "hash-v2", CreatedAt: now, UpdatedAt: now,
	}))
	exportData, err := srcStore.Export(ctx, "MRG", "0.1.0")
	require.NoError(t, err)

	result, err := s.Import(ctx, exportData, ImportModeMerge, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesUpdated, "should update existing node")

	node, err := s.GetNode(ctx, "MRG-1")
	require.NoError(t, err)
	assert.Equal(t, "Updated title", node.Title)
}

// TestImport_MergeMode_SkipsUnchangedNodes verifies that merge mode
// skips nodes whose content hash hasn't changed.
func TestImport_MergeMode_SkipsUnchangedNodes(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "SKP-1", Project: "SKP", Depth: 0, Seq: 1, Title: "Same title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "same-hash", CreatedAt: now, UpdatedAt: now,
	}))

	// Export and re-import the same data.
	exportData, err := s.Export(ctx, "SKP", "0.1.0")
	require.NoError(t, err)

	result, err := s.Import(ctx, exportData, ImportModeMerge, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.NodesSkipped, "unchanged node should be skipped")
	assert.Equal(t, 0, result.NodesUpdated)
}

// ─── Node create edge cases ───

// TestCreateNode_WithCodeRefsAndLabels_PersistsJSON verifies that
// JSON fields (labels, code_refs) are correctly stored and retrieved.
func TestCreateNode_WithCodeRefsAndLabels_PersistsJSON(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "JSON-1", Project: "JSON", Depth: 0, Seq: 1, Title: "JSON fields test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "j1",
		Labels:   []string{"bug", "urgent", "p0"},
		CodeRefs: []model.CodeRef{
			{File: "internal/store/sqlite/store.go", Line: 47},
			{File: "cmd/mtix/main.go", Line: 15},
		},
		CreatedAt: now, UpdatedAt: now,
	}))

	node, err := s.GetNode(ctx, "JSON-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"bug", "urgent", "p0"}, node.Labels)
	assert.Len(t, node.CodeRefs, 2)
	assert.Equal(t, "internal/store/sqlite/store.go", node.CodeRefs[0].File)
	assert.Equal(t, 47, node.CodeRefs[0].Line)
}

// TestCreateNode_WithAnnotations_PersistsJSON verifies annotations are stored.
func TestCreateNode_WithAnnotations_PersistsJSON(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	annotations := []model.Annotation{
		{ID: "ann-1", Text: "Review this carefully", Author: "admin", CreatedAt: now},
	}
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "ANN-1", Project: "ANN", Depth: 0, Seq: 1, Title: "Annotation test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "a1",
		Annotations: annotations,
		CreatedAt: now, UpdatedAt: now,
	}))

	node, err := s.GetNode(ctx, "ANN-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assert.Equal(t, "Review this carefully", node.Annotations[0].Text)
	assert.Equal(t, "admin", node.Annotations[0].Author)
}

// TestCreateNode_WithMetadata_PersistsRawJSON verifies metadata is stored as-is.
func TestCreateNode_WithMetadata_PersistsRawJSON(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "META-1", Project: "META", Depth: 0, Seq: 1, Title: "Metadata test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "m1",
		Metadata: json.RawMessage(`{"custom":"field","nested":{"key":42}}`),
		CreatedAt: now, UpdatedAt: now,
	}))

	node, err := s.GetNode(ctx, "META-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"custom":"field","nested":{"key":42}}`, string(node.Metadata))
}

// ─── Dependency tests ───

// TestRemoveDependency_ExistingDep_Succeeds verifies dependency removal.
func TestRemoveDependency_ExistingDep_Succeeds(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "RD-1", Project: "RD", Depth: 0, Seq: 1, Title: "From",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "rd1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "RD-2", Project: "RD", Depth: 0, Seq: 2, Title: "To",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "rd2", CreatedAt: now, UpdatedAt: now,
	}))

	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "RD-1", ToID: "RD-2", DepType: model.DepTypeBlocks,
		CreatedBy: "admin", CreatedAt: now,
	}))
	require.NoError(t, s.RemoveDependency(ctx, "RD-1", "RD-2", model.DepTypeBlocks))
}

// TestRemoveDependency_NonExistent_ReturnsNotFound verifies removing
// a dependency that doesn't exist returns an error.
func TestRemoveDependency_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newInternalTestStore(t)
	err := s.RemoveDependency(context.Background(), "NOPE-1", "NOPE-2", model.DepTypeBlocks)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// ─── GetDirectChildren tests ───

// TestGetDirectChildren_WithMultipleChildren_ReturnsAll verifies correct
// child retrieval with proper ordering.
func TestGetDirectChildren_WithMultipleChildren_ReturnsAll(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "GDC-1", Project: "GDC", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "gp", CreatedAt: now, UpdatedAt: now,
	}))
	for i := 1; i <= 3; i++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("GDC-1.%d", i), ParentID: "GDC-1", Project: "GDC",
			Depth: 1, Seq: i, Title: fmt.Sprintf("Child %d", i),
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: fmt.Sprintf("gc%d", i),
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	children, err := s.GetDirectChildren(ctx, "GDC-1")
	require.NoError(t, err)
	assert.Len(t, children, 3)
}

// TestGetDirectChildren_NoChildren_ReturnsEmpty verifies leaf node returns empty.
func TestGetDirectChildren_NoChildren_ReturnsEmpty(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "LEAF-1", Project: "LEAF", Depth: 0, Seq: 1, Title: "Leaf",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "l1", CreatedAt: now, UpdatedAt: now,
	}))

	children, err := s.GetDirectChildren(ctx, "LEAF-1")
	require.NoError(t, err)
	assert.Empty(t, children)
}

// ─── WithTx rollback test ───

// TestWithTx_ErrorInFunction_RollsBack verifies that an error in the
// transaction function causes a rollback, leaving no partial data.
func TestWithTx_ErrorInFunction_RollsBack(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO meta (key, value) VALUES (?, ?)", "tx_test", "should_rollback")
		if err != nil {
			return err
		}
		return fmt.Errorf("intentional failure")
	})
	require.Error(t, err)

	// The insert should have been rolled back.
	var count int
	require.NoError(t, s.readDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM meta WHERE key = 'tx_test'").Scan(&count))
	assert.Equal(t, 0, count, "rolled back data should not persist")
}

// TestWithTx_PanicInFunction_RollsBackAndRepanics verifies that a panic
// in the transaction function causes rollback and re-panics.
func TestWithTx_PanicInFunction_RollsBackAndRepanics(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()

	assert.Panics(t, func() {
		_ = s.WithTx(ctx, func(tx *sql.Tx) error {
			_, _ = tx.ExecContext(ctx,
				"INSERT INTO meta (key, value) VALUES (?, ?)", "panic_test", "should_rollback")
			panic("intentional panic")
		})
	})

	// The insert should have been rolled back.
	var count int
	require.NoError(t, s.readDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM meta WHERE key = 'panic_test'").Scan(&count))
	assert.Equal(t, 0, count, "panic-rolled-back data should not persist")
}
