// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// discardUnsharedEvents does nothing on a store that synced: its events may
// be known to the hub, so none is deleted, whatever the file holds
// (MTIX-107.31.3). The replace refuses such a store before this runs; this
// pins the guard in the function itself.
func TestDiscardUnsharedEvents_StoreThatSynced_DeletesNothing(t *testing.T) {
	ctx := context.Background()
	s, raw, _ := reconcileTestStore(t)
	seedTree(t, s)
	// MTIX-1's creation was pushed: the store has synced. MTIX-2's is pending.
	_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE node_id = 'MTIX-1'`)
	require.NoError(t, err)
	var before int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE node_id = 'MTIX-2'`).Scan(&before))
	require.NotZero(t, before)

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return discardUnsharedEvents(ctx, tx, &ExportData{}, false) // a file that holds nothing
	}))
	var after int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events WHERE node_id = 'MTIX-2'`).Scan(&after))
	assert.Equal(t, before, after, "a store that synced keeps every event")
}
