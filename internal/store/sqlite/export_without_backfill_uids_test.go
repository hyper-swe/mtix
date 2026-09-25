// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.11 (FR-15.2h, FR-7.8): WithoutBackfillUIDs returns an
// export as this version wrote the same store before the open-time backfill
// (MTIX-95.31.9) gave its tasks without a uid one, so a conflict baseline
// written then can be recognized. Written red-first.
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

// exportJSON returns the export's JSON encoding without its export time.
func exportJSON(t *testing.T, data *sqlite.ExportData) string {
	t.Helper()
	content := *data
	content.ExportedAt = ""
	raw, err := json.Marshal(&content)
	require.NoError(t, err)
	return string(raw)
}

// TestWithoutBackfillUIDs_BackfilledTask_ExportAsBeforeTheBackfill verifies
// only backfill uids are left out, and the result, checksum included, is
// the export of the same store before the backfill: COL-2 had no uid, and
// a uid a create minted (COL-1) or no uid at all (COL-3) stays as it is.
// The export passed in is not changed.
func TestWithoutBackfillUIDs_BackfilledTask_ExportAsBeforeTheBackfill(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for i, id := range []string{"COL-1", "COL-2", "COL-3"} {
		createEveryColumnNode(t, s, id, i+1)
	}
	exec := func(query string, args ...any) {
		_, err := s.WriteDB().ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	exec(`UPDATE nodes SET uid = NULL WHERE id IN ('COL-2', 'COL-3')`)
	beforeBackfill, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	backfill, err := model.NewBackfillUID()
	require.NoError(t, err)
	exec(`UPDATE nodes SET uid = ? WHERE id = 'COL-2'`, backfill)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	before := exportJSON(t, data)

	form, err := sqlite.WithoutBackfillUIDs(data)
	require.NoError(t, err)

	assert.Equal(t, exportJSON(t, beforeBackfill), exportJSON(t, form))
	ok, err := sqlite.VerifyExportChecksum(form)
	require.NoError(t, err)
	assert.True(t, ok, "the result carries its own checksum")
	assert.Equal(t, before, exportJSON(t, data), "the export passed in is not changed")
}

// TestWithoutBackfillUIDs_NoBackfillUID_SameExport verifies an export
// without backfill uids comes back as it is, checksum included.
func TestWithoutBackfillUIDs_NoBackfillUID_SameExport(t *testing.T) {
	s := newTestStore(t)
	createEveryColumnNode(t, s, "COL-1", 1)
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)

	form, err := sqlite.WithoutBackfillUIDs(data)
	require.NoError(t, err)

	assert.Equal(t, exportJSON(t, data), exportJSON(t, form))
}

// TestWithoutBackfillUIDs_NilExport_ReturnsInvalidInput verifies a nil
// export is refused.
func TestWithoutBackfillUIDs_NilExport_ReturnsInvalidInput(t *testing.T) {
	_, err := sqlite.WithoutBackfillUIDs(nil)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}
