// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
)

const hostileLoserEventID = "lo-\x07bell\x1b[2J中"

// seedHostileConflict inserts one unresolved conflict whose winner and loser
// ids are hostile, as a pulled hub event can make them (MTIX-187.4.2.1).
func seedHostileConflict(t *testing.T) {
	t.Helper()
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `
			INSERT INTO sync_conflicts
			  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at, resolved_by)
			VALUES (?, ?, 'TEST-1', 'title', 'lww', '2026-09-25T00:00:00Z', NULL)`,
			hostileEventID, hostileLoserEventID)
		return err
	}))
}

// The text table names the winner and loser ids only in their safe rendering
// (printable ASCII); --json keeps the stored ids byte for byte.
func TestPrintConflictsTable_HostileEventIDs_RenderPrintableASCII(t *testing.T) {
	initTestApp(t)
	seedHostileConflict(t)

	var text, stderr bytes.Buffer
	require.NoError(t, runSyncConflictsList(t.Context(), &text, &stderr, "", false))
	requirePrintableASCII(t, text.String(), hostileEventID)
	require.Contains(t, text.String(), "winner="+format.EventID(hostileEventID)+" loser="+format.EventID(hostileLoserEventID)+"\n",
		"winner and loser each sit in their own column")

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	var out bytes.Buffer
	require.NoError(t, runSyncConflictsList(t.Context(), &out, &stderr, "", false))
	var rows []ConflictRow
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, hostileEventID, rows[0].EventIDWinner)
	require.Equal(t, hostileLoserEventID, rows[0].EventIDLoser)
	want, err := json.MarshalIndent(rows, "", "  ")
	require.NoError(t, err)
	require.Equal(t, string(want)+"\n", out.String(), "--json is the plain encoding of the stored rows")
}
