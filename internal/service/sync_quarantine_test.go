// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestSyncService_ListQuarantined_EveryRowInRetryOrder: the read-only view
// behind `mtix sync quarantine list` (MTIX-95.11) returns every quarantined
// event, across more than one internal page, in retry order (Lamport clock,
// then event id), with the node and op read from the raw event.
func TestSyncService_ListQuarantined_EveryRowInRetryOrder(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	ctx := context.Background()
	const n = 1201
	require.NoError(t, st.WithTx(ctx, func(tx *sql.Tx) error {
		for i := n; i >= 1; i-- {
			raw := fmt.Sprintf(`{"event_id":"e%05d","node_id":"TEST-%d","op_type":"update_field","lamport_clock":%d}`, i, i, i)
			if err := sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
				EventID: fmt.Sprintf("e%05d", i), Source: "sweep", RawEvent: raw, Reason: "why",
				FirstSeen: "2026-09-25T00:00:00Z", LastAttempt: "2026-09-25T01:00:00Z", CLIVersion: "v",
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	got, err := svc.ListQuarantined(ctx)

	require.NoError(t, err)
	require.Len(t, got, n)
	for i, q := range got {
		require.Equal(t, fmt.Sprintf("e%05d", i+1), q.EventID)
	}
	require.Equal(t, service.QuarantinedEvent{
		EventID: "e00007", NodeID: "TEST-7", OpType: "update_field", LamportClock: 7, Source: "sweep",
		Reason: "why", Attempts: 1, FirstSeen: "2026-09-25T00:00:00Z", LastAttempt: "2026-09-25T01:00:00Z",
		CLIVersion: "v",
	}, got[6])
}

// TestSyncService_ListQuarantined_Empty_ReturnsEmptySlice: no quarantine is
// an empty, non-nil list (JSON `[]`).
func TestSyncService_ListQuarantined_Empty_ReturnsEmptySlice(t *testing.T) {
	svc, _, _ := newTestSyncService(t)

	got, err := svc.ListQuarantined(context.Background())

	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
}
