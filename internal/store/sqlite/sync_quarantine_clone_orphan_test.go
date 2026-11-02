// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedOrphanRows quarantines four pulled rows with different reasons and one
// held push row that carries the clone-orphan marker (MTIX-95.37.1).
func seedOrphanRows(t *testing.T, s *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	rows := []sqlite.QuarantinedEvent{
		{EventID: "orphan-start", Source: "pull", Reason: sqlite.CloneOrphanReason + ": node X: not found"},
		{EventID: "orphan-sweep", Source: "sweep", Reason: sqlite.CloneOrphanReason},
		{EventID: "marker-middle", Source: "pull", Reason: "x " + sqlite.CloneOrphanReason},
		{EventID: "plain", Source: "pull", Reason: "apply failed"},
	}
	quarantineTx(t, s, func(tx *sql.Tx) error {
		for _, q := range rows {
			q.RawEvent, q.FirstSeen, q.LastAttempt = `{"lamport_clock":1}`, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"
			if err := sqlite.QuarantineEvent(ctx, tx, q); err != nil {
				return err
			}
		}
		return nil
	})
	hold := pushHold("held-with-marker")
	hold.Reason = sqlite.CloneOrphanReason
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{hold}, "v"))
}

func TestCountQuarantinedCloneOrphans_MarkerAtStartOfPulledReasonOnly(t *testing.T) {
	s := newTestStore(t)
	seedOrphanRows(t, s)

	n, err := s.CountQuarantinedCloneOrphans(context.Background())

	require.NoError(t, err)
	require.Equal(t, 2, n, "the pull and sweep rows that start with the marker; not a mid-reason marker, not a push hold")
}

func TestClearQuarantineExceptCloneOrphans_KeepsOrphansAndPushHolds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	seedOrphanRows(t, s)

	require.NoError(t, s.ClearQuarantineExceptCloneOrphans(ctx))

	page, err := s.QuarantinePage(ctx, nil, 10)
	require.NoError(t, err)
	var ids []string
	for _, q := range page {
		ids = append(ids, q.EventID)
	}
	require.ElementsMatch(t, []string{"orphan-start", "orphan-sweep", "held-with-marker"}, ids)
}
