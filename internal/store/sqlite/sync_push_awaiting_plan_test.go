// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPendingCreationSQL_Plan_NeverScansTheEventLog pins the plan of the one
// predicate behind push's creation gate and pull's pending-number guard
// (MTIX-95.37). A scan of sync_events here, as an OR inside a correlated
// EXISTS caused, costs time proportional to the log for every batch (about
// 560 ms per 100 events at 20k creations). Rule 1b: the EXPLAIN QUERY PLAN
// prefix is applied, in a test, to the package's own constant SQL, with the
// arguments still bound.
func TestPendingCreationSQL_Plan_NeverScansTheEventLog(t *testing.T) {
	s := newInternalTestStore(t)
	ctx := context.Background()
	rows, err := s.readDB.QueryContext(ctx, "EXPLAIN QUERY PLAN "+pendingCreationSQL, `["PROJ-1"]`, `["PROJ-1"]`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	text := strings.Join(plan, "\n")
	require.NotContains(t, text, "SCAN c", "the log is searched through an index, never scanned:\n"+text)
	require.NotContains(t, text, "SCAN sync_events", text)
	require.Contains(t, text, "SEARCH c USING", text)
	require.Contains(t, text, "idx_sync_events_uid", "the adopted-uid branch uses the partial uid index:\n"+text)
}
