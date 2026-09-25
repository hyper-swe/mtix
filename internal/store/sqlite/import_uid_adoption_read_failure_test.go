// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// nodeRowsSnapshot returns every node's id, uid and title as one string,
// read through the write connection with the title column renamed.
func nodeRowsSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	// Every node row the import could change, in id order.
	rows, err := s.writeDB.QueryContext(context.Background(),
		`SELECT id || '|' || COALESCE(uid, '') || '|' || title_unreadable || '|' || COALESCE(content_hash, '')
		   FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out string
	for rows.Next() {
		var row string
		require.NoError(t, rows.Scan(&row))
		out += row + "\n"
	}
	require.NoError(t, rows.Err())
	return out
}

// TestImportReconcile_AdoptionPlanCannotReadLocalNodes_ReturnsItsErrorWritesNothing
// verifies a local read that fails while a merge plans its uid adoptions
// (MTIX-95.31.6, tested by MTIX-95.31.9) stops the import with that error,
// wrapped, before anything is written: the adoption report is never left
// empty in silence. The failure is made by renaming the title column,
// which only the adoption plan reads before the write.
func TestImportReconcile_AdoptionPlanCannotReadLocalNodes_ReturnsItsErrorWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := newInternalTestStore(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Local title",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-local", CreatedAt: created, UpdatedAt: created,
	}))
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data.Nodes[0].Title, data.Nodes[0].ContentHash = "File title", "h-file"
	require.NoError(t, RecomputeExportChecksum(data))
	// From here on, every read of nodes.title fails.
	_, err = s.writeDB.ExecContext(ctx, `ALTER TABLE nodes RENAME COLUMN title TO title_unreadable`)
	require.NoError(t, err)
	before := nodeRowsSnapshot(t, s)

	report, result, err := s.ImportReconcile(ctx, data, ImportReconcileOptions{Mode: ImportModeMerge})
	require.Error(t, err)
	assert.ErrorContains(t, err, "read the local nodes for the uid adoptions")
	assert.ErrorContains(t, err, "title", "the read error is wrapped, not replaced")
	assert.Nil(t, result)
	require.NotNil(t, report)
	assert.False(t, report.Applied)
	assert.Equal(t, before, nodeRowsSnapshot(t, s), "nothing was written")
}
