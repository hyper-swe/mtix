// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.2: the history query and the loss warnings of the retention guarantee.

func TestInboxHistoryCLI_TextAndJSON_ListsStatesAndAckTimes(t *testing.T) {
	initTestApp(t)
	events := envelopeCLIEvents(t, "first", "second", "third")
	require.NoError(t, app.store.InboxAck(t.Context(), "worker", events[1].Seq))

	text := outputInboxCommand(t, "--agent", "worker", "--history")
	assert.Contains(t, text, "unacked")
	assert.NotContains(t, text, "watermark")
	assert.Contains(t, text, "acked at ")
	assert.Contains(t, text, "first")
	assert.Contains(t, text, "third")
	// Reading history never acknowledges anything.
	after, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	assert.Len(t, after, 2)

	app.jsonOutput = true
	var got []sqlite.InboxHistoryEntry
	require.NoError(t, json.Unmarshal([]byte(outputInboxCommand(t, "--agent", "worker", "--history")), &got))
	require.Len(t, got, 3)
	assert.Equal(t, sqlite.InboxUnacked, got[0].State)
	assert.Equal(t, sqlite.InboxAcked, got[1].State)
	require.NotNil(t, got[1].AckedAt)
	assert.Nil(t, got[0].AckedAt)

	require.NoError(t, app.store.InboxAckThrough(t.Context(), "worker", events[0].Seq))
	app.jsonOutput = false
	assert.Contains(t, outputInboxCommand(t, "--agent", "worker", "--history"), "acked through a watermark (time not kept)")

	app.jsonOutput = true
	none := outputInboxCommand(t, "--agent", "nobody", "--history")
	assert.JSONEq(t, `[]`, none)
}

func TestInboxHistoryCLI_ConflictingFlags_Refused(t *testing.T) {
	for _, extra := range []string{"--wait", "--format=prompt", "--count"} {
		t.Run(extra, func(t *testing.T) {
			initTestApp(t)
			cmd := newInboxCmd()
			cmd.SetArgs([]string{"--agent", "worker", "--history", extra})
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			_, err := cliBehaviorOutput(t, cmd.Execute)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--history cannot be combined with")
		})
	}
}

// The history is local: it must not open a hub connection even when a hub
// is configured. A decoy listener stands in for the hub and counts every
// connection attempt.
func TestInboxHistoryCLI_WithHubConfigured_OpensNoTransport(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int64
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	t.Setenv(transport.EnvDSN, "postgres://u:p@"+ln.Addr().String()+"/hub?sslmode=disable")

	root := newRootCmd()
	root.SetArgs([]string{"inbox", "--agent", "worker", "--history"})
	require.NoError(t, app.store.Close())
	app.store = nil
	resetCloseOnce()
	out, err := cliBehaviorOutput(t, root.Execute)
	require.NoError(t, err)
	assert.Contains(t, out, "mail")
	assert.Zero(t, accepted.Load(), "history opened a connection to the configured hub")
}

func TestDestructiveOps_CountUnackedInboxEvents(t *testing.T) {
	initTestApp(t)
	events := envelopeCLIEvents(t, "one", "two", "three")
	require.NoError(t, app.store.InboxAck(t.Context(), "worker", events[0].Seq))

	for name, build := range map[string]func() (destructiveOp, error){
		"discard-local":  func() (destructiveOp, error) { return discardLocalOp(t.Context()) },
		"import replace": func() (destructiveOp, error) { return importReplaceOp(t.Context(), 0) },
	} {
		t.Run(name, func(t *testing.T) {
			op, err := build()
			require.NoError(t, err)
			var found *destroyCount
			for i := range op.Destroys {
				if op.Destroys[i].Label == unackedInboxLabel {
					found = &op.Destroys[i]
				}
			}
			require.NotNil(t, found, "the destroys list names the unread mail: %v", op.Destroys)
			assert.Equal(t, 2, found.N)
			assert.NotEqual(t, unackedInboxLabel, op.Destroys[0].Label, "the typed token stays the ticket count")
		})
	}
}

func TestExportInboxNote_PrintsOnlyWhenUnacked(t *testing.T) {
	var w bytes.Buffer
	writeExportInboxNote(&w, 0)
	assert.Empty(t, w.String())
	writeExportInboxNote(&w, 3)
	assert.Contains(t, w.String(), "3 unacknowledged inbox event(s)")
	assert.Contains(t, w.String(), "does not carry them")
	assert.Contains(t, w.String(), "mtix inbox --agent <id> --history")
}

func TestRunExport_UnackedInbox_NotesOnStderr(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail")
	var errBuf bytes.Buffer
	require.NoError(t, exportInboxNoteTo(t.Context(), &errBuf))
	assert.Contains(t, errBuf.String(), "1 unacknowledged inbox event(s)")
}

func TestRecoverInboxNote_CountsFromDamagedDatabaseOrSaysUnknown(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail")
	var w bytes.Buffer
	writeRecoverInboxNote(t.Context(), &w, app.mtixDir+"/data/mtix.db")
	assert.Contains(t, w.String(), "1 unacknowledged inbox event(s)")
	w.Reset()
	writeRecoverInboxNote(t.Context(), &w, app.mtixDir+"/data/missing.db")
	assert.Contains(t, w.String(), "could not be counted")
}

// History is a local read like count: a changed tasks.json is not imported by it.
func TestInboxHistoryStartup_ChangedBoard_SkipsOnlyHistoryImport(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	raw, err := os.ReadFile(filepath.Join(".mtix", "tasks.json"))
	require.NoError(t, err)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &data))
	require.NoError(t, initApp(newInboxCmd(), ""))
	node, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	data.Nodes[0].Annotations = node.Annotations
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err = json.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(".mtix", "tasks.json"), raw, 0600))
	require.NoError(t, app.store.Close())
	app.store = nil
	cmd := newInboxCmd()
	cmd.SetContext(t.Context())
	require.NoError(t, cmd.Flags().Parse([]string{"--history", "--agent", "worker"}))
	require.NoError(t, persistentPreRun(cmd, nil, ""))
	_, err = app.store.GetNode(t.Context(), "TEST-2")
	require.ErrorIs(t, err, model.ErrNotFound)
	require.NoError(t, app.store.Close())
	app.store = nil
	ordinary := newInboxCmd()
	ordinary.SetContext(t.Context())
	require.NoError(t, persistentPreRun(ordinary, nil, ""))
	_, err = app.store.GetNode(t.Context(), "TEST-2")
	require.NoError(t, err)
}
