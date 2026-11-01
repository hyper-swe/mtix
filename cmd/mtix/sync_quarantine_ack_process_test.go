//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func writeUnrelatedQuarantineImport(t *testing.T) []byte {
	t.Helper()
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-1", "", 0, 1, "original task")))
	require.NoError(t, app.syncSvc.ForceExport(t.Context(), app.mtixDir))
	board, err := app.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	board.Nodes[0].Title = "unrelated incoming task edit"
	require.NoError(t, sqlite.RecomputeExportChecksum(board))
	raw, err := json.Marshal(board)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "tasks.json"), raw, 0o644))
	return raw
}

func TestSyncQuarantineAcknowledge_ActualProcess_NoDSNNoTTYSkipsUnrelatedImport(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	initAckCLI(t)
	operator := operatorTestHome(t)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir)}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID=cli-operator", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	incoming := writeUnrelatedQuarantineImport(t)
	before := readPeerState(t, &sweepFixture{})
	out := f.run(t, "sync", "quarantine", "acknowledge", ackCLIEventID, "--reason", "reviewed", "--json")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var receipt service.QuarantineAcknowledgement
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &receipt))
	require.Equal(t, "cli-operator", receipt.Actor)
	require.Equal(t, "reviewed", receipt.Reason)
	require.Equal(t, before, readPeerState(t, &sweepFixture{}))
	node, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "original task", node.Title)
	after, err := os.ReadFile(filepath.Join(app.mtixDir, "tasks.json"))
	require.NoError(t, err)
	require.Equal(t, incoming, after, "local acknowledgement neither imports nor exports the board")
	listed, err := app.syncSvc.ListQuarantined(t.Context())
	require.NoError(t, err)
	require.Equal(t, &receipt, listed[0].Acknowledgement)
}

func newLocalQuarantineRecoveryCLI(t *testing.T, binary string) quarantineRecoveryCLI {
	t.Helper()
	operator := operatorTestHome(t)
	return quarantineRecoveryCLI{
		binary: binary, project: filepath.Dir(app.mtixDir),
		env: append(hostguard.Environ(), "MTIX_AUTHOR_ID=cli-operator", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator),
	}
}

func TestSyncQuarantineAcknowledge_ActualProcess_NegativeControls(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	initAckCLI(t)
	f := newLocalQuarantineRecoveryCLI(t, binary)
	seedAckCLI(t, "push", "push", `{"event_id":"push","node_id":"TEST-1é"}`, "invalid node id")
	seedAckCLI(t, "forged", "pull", `{"event_id":"forged","node_id":"TEST-2"}`, "invalid node id")
	seedAckCLI(t, "malformed", "pull", "not-json", "invalid node id")
	seedAckCLI(t, "mismatch", "pull", ackCLIRaw, "invalid node id")
	seedAckCLI(t, "missing-node", "pull", `{"event_id":"missing-node","op_type":"create_node","payload":{}}`, "invalid node id")
	marked := strings.Replace(ackCLIRaw, ackCLIEventID, "marked", 1)
	marked = strings.TrimSuffix(marked, "}") + `,"malformed":"hub row did not decode"}`
	seedAckCLI(t, "marked", "pull", marked, "invalid node id")
	before := readPeerState(t, &sweepFixture{})
	for _, args := range [][]string{
		{ackCLIEventID},
		{ackCLIEventID, "--reason", " \t\n "},
		{"unknown", "--reason", "reviewed"},
		{"push", "--reason", "reviewed"},
		{"forged", "--reason", "reviewed"},
		{"malformed", "--reason", "reviewed"},
		{"mismatch", "--reason", "reviewed"},
		{"missing-node", "--reason", "reviewed"},
		{"marked", "--reason", "reviewed"},
	} {
		out := f.run(t, append([]string{"sync", "quarantine", "acknowledge"}, args...)...)
		require.NotZero(t, out.exitCode, "must refuse %v: %s", args, out.stdout)
	}
	require.Equal(t, before, readPeerState(t, &sweepFixture{}))
	counts, err := app.store.CountQuarantineAcknowledgements(t.Context())
	require.NoError(t, err)
	require.Zero(t, counts.Acknowledged)
	ok, _ := checkQuarantinedEvents(t.Context(), app.store)
	require.False(t, ok)
}
