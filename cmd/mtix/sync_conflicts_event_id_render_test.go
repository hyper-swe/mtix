// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
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

// Collision task uids come from sync and must be rendered whole (MTIX-187.4.12).
func TestPrintCollisions_HostileUIDs_RenderWhole(t *testing.T) {
	for _, uid := range []string{"TEST-plain-long-uid", hostileEventID, hostileLoserEventID, "1234567é-tail", ""} {
		t.Run(fmt.Sprintf("%q", uid), func(t *testing.T) {
			rows := []transport.OpenCollision{{DisplayPath: "TEST-1", HeldUID: uid, IncomingUID: uid + "-incoming"}}
			before := rows[0]
			var out bytes.Buffer
			require.NoError(t, writeCollisions(&out, rows, false))
			require.Contains(t, out.String(), "held     uid="+format.EventID(uid)+" epoch=")
			require.Contains(t, out.String(), "incoming uid="+format.EventID(uid+"-incoming")+" epoch=")
			for _, line := range strings.Split(out.String(), "\n") {
				// Every line of the table is printable ASCII, except the fixed
				// prose banner (it holds an em dash and carries no stored id).
				if !strings.Contains(line, "open restore collision(s)") {
					printableASCIILines(t, line)
				}
			}
			require.Equal(t, before, rows[0], "rendering must not mutate the source")
		})
	}
}

func TestWriteCollisions_JSON_PreservesUIDBytes(t *testing.T) {
	rows := []transport.OpenCollision{{DisplayPath: "TEST-1", HeldUID: hostileEventID, IncomingUID: hostileLoserEventID}}
	var out bytes.Buffer
	require.NoError(t, writeCollisions(&out, rows, true))
	var got []transport.OpenCollision
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, rows, got)
	want, err := json.MarshalIndent(rows, "", "  ")
	require.NoError(t, err)
	require.Equal(t, string(want)+"\n", out.String())
}
