// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
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
	for _, extra := range []string{"--wait", "--format=prompt", "--count", "--all", "--all=false"} {
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

// captureStderrOf runs call with os.Stderr redirected to a file and returns it.
func captureStderrOf(t *testing.T, call func() error) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr-")
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = f
	callErr := call()
	os.Stderr = old
	require.NoError(t, f.Close())
	data, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	return string(data), callErr
}

// The export command, end to end: the JSON stays alone on stdout, the note
// with the count goes to stderr, and a failed count never fails the export.
func TestExportCommand_UnackedInbox_NoteOnStderrJSONOnStdout(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail one", "mail two")
	var stdout string
	stderr, err := captureStderrOf(t, func() error {
		var runErr error
		stdout, runErr = cliBehaviorOutput(t, func() error { return runExport("json") })
		return runErr
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "2 unacknowledged inbox event(s)")
	assert.NotContains(t, stdout, "unacknowledged")
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(stdout), &data), "stdout is the export document alone")
}

func TestExportCommand_NoUnackedInbox_PrintsNoNote(t *testing.T) {
	initTestApp(t)
	events := envelopeCLIEvents(t, "mail")
	require.NoError(t, app.store.InboxAck(t.Context(), "worker", events[0].Seq))
	stderr, err := captureStderrOf(t, func() error {
		_, runErr := cliBehaviorOutput(t, func() error { return runExport("json") })
		return runErr
	})
	require.NoError(t, err)
	assert.NotContains(t, stderr, "unacknowledged")
}

func TestExportCommand_CountFails_ExportStillSucceedsWithWarning(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail")
	_, err := app.store.WriteDB().ExecContext(t.Context(), `DROP TABLE agent_inbox_ack`)
	require.NoError(t, err)
	var stdout string
	stderr, err := captureStderrOf(t, func() error {
		var runErr error
		stdout, runErr = cliBehaviorOutput(t, func() error { return runExport("json") })
		return runErr
	})
	require.NoError(t, err, "a finished export must not fail because the note could not be counted")
	assert.Contains(t, stderr, "could not count unacknowledged inbox events")
	assert.Contains(t, stdout, `"nodes"`)
}

// The recover command, end to end: it names the unread mail of the damaged
// database, and says nothing when there is none.
func TestRecoverCommand_UnackedInbox_NamesCountAfterReport(t *testing.T) {
	initTestApp(t)
	events := envelopeCLIEvents(t, "mail one", "mail two")
	out, err := cliBehaviorOutput(t, func() error { _, runErr := runRecover(); return runErr })
	require.NoError(t, err)
	assert.Contains(t, out, "the damaged database holds 2 unacknowledged inbox event(s)")
	assert.Contains(t, out, "Salvage complete")

	for _, e := range events {
		require.NoError(t, app.store.InboxAck(t.Context(), "worker", e.Seq))
	}
	out, err = cliBehaviorOutput(t, func() error { _, runErr := runRecover(); return runErr })
	require.NoError(t, err)
	assert.NotContains(t, out, "unacknowledged")
}

func TestRecoverCommand_UnreadableCount_SaysCouldNotCount(t *testing.T) {
	var w bytes.Buffer
	path := filepath.Join(t.TempDir(), "never-created.db")
	writeRecoverInboxNote(t.Context(), &w, path)
	assert.Contains(t, w.String(), "could not be counted")
	assert.NoFileExists(t, path)
}

// History stays local where a plain inbox read may route to a live server;
// that is the difference between the two paths.
func TestInboxHistoryStartup_LiveServer_StaysLocal(t *testing.T) {
	initTestApp(t)
	require.NoError(t, writePIDLock(app.mtixDir, 6849))
	for _, tt := range []struct {
		args []string
		want int
	}{{nil, 6849}, {[]string{"--history"}, 0}, {[]string{"--history=false"}, 6849}} {
		cmd := newInboxCmd()
		require.NoError(t, cmd.Flags().Parse(tt.args))
		require.Equal(t, tt.want, shouldRouteToServer(cmd), tt.args)
	}
	cmd := newInboxCmd()
	require.NoError(t, cmd.Flags().Parse([]string{"--history"}))
	assert.True(t, isExemptCommand(cmd))
	plain := newInboxCmd()
	require.NoError(t, plain.Flags().Parse(nil))
	assert.False(t, isExemptCommand(plain))
}

// The required name: discard-local warns of the unread mail with a count, and
// the manual states the loss.
func TestInboxRetention_DiscardLocal_WarnsAndDocumented(t *testing.T) {
	manualPath, err := filepath.Abs(filepath.Join("..", "..", "USERMANUAL.md")) // before the fixture changes directory
	require.NoError(t, err)
	initTestApp(t)
	envelopeCLIEvents(t, "mail one", "mail two")
	op, err := discardLocalOp(t.Context())
	require.NoError(t, err)
	var w bytes.Buffer
	answerDestructive(t, "wrong", true)
	require.ErrorIs(t, confirmDestructive(&w, op), errDestructiveRefused)
	assert.Contains(t, w.String(), "2 "+unackedInboxLabel, "the warning printed before the prompt carries the count")

	manual, err := os.ReadFile(manualPath)
	require.NoError(t, err)
	_, section, ok := strings.Cut(string(manual), "## Inbox retention guarantee\n")
	require.True(t, ok)
	section, _, _ = strings.Cut(section, "\n## ")
	assert.Contains(t, section, "--discard-local")
	assert.Contains(t, section, "lists the unacknowledged inbox events first")
}
