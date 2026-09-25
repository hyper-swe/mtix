// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRunSyncQuarantineList_TextAndJSON: `mtix sync quarantine list` is the
// read-only way to inspect the quarantine (MTIX-95.11): one row per event
// with its id, node, op, attempts, first seen and last attempt times and
// reason; `--json` gives the same as an array, `[]` when empty.
func TestRunSyncQuarantineList_TextAndJSON(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	var out bytes.Buffer

	app.jsonOutput = true
	require.NoError(t, runSyncQuarantineList(ctx, &out))
	require.JSONEq(t, `[]`, out.String())
	out.Reset()
	app.jsonOutput = false
	require.NoError(t, runSyncQuarantineList(ctx, &out))
	require.Equal(t, "no quarantined events\n", out.String())

	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
			EventID: "0193fb00-0000-7000-8000-000000000001", Source: "pull",
			RawEvent:  `{"node_id":"TEST-3","op_type":"link_dep","lamport_clock":4}`,
			Reason:    "apply: FOREIGN KEY constraint failed",
			FirstSeen: "2026-09-25T01:00:00Z", LastAttempt: "2026-09-25T02:00:00Z", CLIVersion: "0.5.4",
		})
	}))
	out.Reset()
	require.NoError(t, runSyncQuarantineList(ctx, &out))
	require.Regexp(t, regexp.MustCompile(`(?m)^EVENT ID\s+NODE\s+OP\s+ATTEMPTS\s+FIRST SEEN\s+LAST ATTEMPT\s+REASON$`), out.String())
	require.Regexp(t, regexp.MustCompile(`(?m)^0193fb00-0000-7000-8000-000000000001\s+TEST-3\s+link_dep\s+1\s+2026-09-25T01:00:00Z\s+2026-09-25T02:00:00Z\s+apply: FOREIGN KEY constraint failed$`), out.String())

	out.Reset()
	app.jsonOutput = true
	require.NoError(t, runSyncQuarantineList(ctx, &out))
	var got []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Len(t, got, 1)
	require.Equal(t, map[string]any{
		"event_id": "0193fb00-0000-7000-8000-000000000001", "node_id": "TEST-3", "op_type": "link_dep",
		"lamport_clock": float64(4), "source": "pull", "reason": "apply: FOREIGN KEY constraint failed",
		"attempts": float64(1), "first_seen": "2026-09-25T01:00:00Z", "last_attempt": "2026-09-25T02:00:00Z",
		"cli_version": "0.5.4",
	}, got[0])
}

// TestRunSyncQuarantineList_HostileText_PrintedInert: node ids and reasons
// come from hub rows, so the table prints them without control characters.
func TestRunSyncQuarantineList_HostileText_PrintedInert(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
			EventID: "e1", Source: "pull", RawEvent: `{"node_id":"X\u001b[2J","op_type":"claim"}`,
			Reason: "bad\u001b[31m\nreason", FirstSeen: "t", LastAttempt: "t",
		})
	}))
	var out bytes.Buffer

	require.NoError(t, runSyncQuarantineList(ctx, &out))

	require.NotContains(t, out.String(), "\u001b")
	require.Contains(t, out.String(), "X[2J")
	require.Contains(t, out.String(), "bad[31m reason")
}

// TestRunSyncQuarantineList_OutsideProject_Refuses: like the other sync
// commands, it needs an mtix project.
func TestRunSyncQuarantineList_OutsideProject_Refuses(t *testing.T) {
	initTestApp(t)
	app.mtixDir = ""
	err := runSyncQuarantineList(context.Background(), &bytes.Buffer{})
	require.ErrorContains(t, err, "not in an mtix project")
	initTestApp(t)
	app.syncSvc = nil
	err = runSyncQuarantineList(context.Background(), &bytes.Buffer{})
	require.ErrorContains(t, err, "local store not initialized")
}

// TestSyncCmd_RegistersQuarantineList: the command is reachable as
// `mtix sync quarantine list` and takes no arguments.
func TestSyncCmd_RegistersQuarantineList(t *testing.T) {
	cmd, _, err := newSyncCmd().Find([]string{"quarantine", "list"})
	require.NoError(t, err)
	require.Equal(t, "list", cmd.Name())
	require.Error(t, cmd.Args(cmd, []string{"extra"}))
}
