// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// sweepRouteByTestName runs the sweep the readRouteTests table lists for the
// calling test's own name, so a test cannot name another family or carry its
// own iteration count (MTIX-264). TestReadRouteShards_RouteTests_AreBoundToTheirTableEntry
// pins the table and that every route test makes exactly this call.
func sweepRouteByTestName(t *testing.T) {
	t.Helper()
	entry, ok := readRouteTests[t.Name()]
	require.True(t, ok, "%s has no entry in readRouteTests", t.Name())
	sweepCancelledFamily(t, entry.family, entry.iterations)
}

// One top-level test per route family, each with the full
// readCancelRouteIterations sweep, so the CI shard hash (scripts/ci-go-shards.sh,
// djb2 of the test name) can place their cost on chosen SQLite shards instead of
// putting all of it in one: three on shard 1, none on shard 2, five on shard 3,
// by measured shard load (MTIX-264). The shard table is in
// read_cancel_routes_shard_test.go; the sweep and its detection window are
// described at readCancelRouteIterations.

// TestReadRoutes_StoreQuery_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled Store.Query reads (MTIX-250, MTIX-264).
func TestReadRoutes_StoreQuery_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_InboxAckInfo_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled InboxAckInfo reads (MTIX-250, MTIX-264).
func TestReadRoutes_InboxAckInfo_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_ReadDBQuery_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled ReadDB.QueryContext reads (MTIX-250, MTIX-264).
func TestReadRoutes_ReadDBQuery_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_ReadDBConnPrepareCtx_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled ReadDB.Conn.PrepareContext reads (MTIX-250, MTIX-264).
func TestReadRoutes_ReadDBConnPrepareCtx_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_ConnRawPrepareStmt_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled Conn.Raw.Prepare reads (MTIX-250, MTIX-264).
func TestReadRoutes_ConnRawPrepareStmt_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_StoreQueryRow_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled Store.QueryRow reads (MTIX-250, MTIX-264).
func TestReadRoutes_StoreQueryRow_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_ConnPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled Conn.PrepareContext reads (MTIX-250, MTIX-264).
func TestReadRoutes_ConnPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}

// TestReadRoutes_TxPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot sweeps cancelled Tx.PrepareContext reads (MTIX-250, MTIX-264).
func TestReadRoutes_TxPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot(t *testing.T) {
	sweepRouteByTestName(t)
}
