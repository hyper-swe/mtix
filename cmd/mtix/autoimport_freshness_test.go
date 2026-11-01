// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Read-command output reports unverified freshness when sync ownership is busy.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func holdCLIImportLock(t *testing.T) *os.File {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(".mtix", "data", "sync.lock"), os.O_RDWR, 0644)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	t.Cleanup(func() { require.NoError(t, lock.Close()) })
	return lock
}

type busyReadCase struct {
	name     string
	run      func() error
	wantNode bool
}

func busyReadCases() []busyReadCase {
	return []busyReadCase{
		{"show", func() error { return runShow("TEST-1") }, true},
		{"list", func() error { return runList("", "", "", "", "", "", "", "", 0, false, 50, "", false) }, true},
		{"tree", func() error { return runTree("TEST-1", 10) }, true},
		{"ready", func() error { return runReady("", false) }, true},
		{"search", func() error { return runSearch([]string{"Reviewed"}, "", "", "", "", "", "", "", 50, "", false) }, true},
		{"context", func() error { return runContext("TEST-1", 0, false) }, true},
		{"progress", func() error { return runProgress("TEST-1") }, true},
		{"stats", runStats, false},
		{"blocked", func() error { return runBlocked("", false) }, false},
		{"stale", func() error { return runStale("", false) }, false},
		{"orphans", func() error { return runOrphans("", false) }, false},
		{"dep show", func() error { return runDepShow("TEST-1") }, false},
		{"agent work", func() error { return runAgentWork("reader") }, true},
		{"verify", func() error { return runVerify("TEST-1") }, true},
		{"inbox", func() error { return runInbox("reader", false, 0, "") }, false},
	}
}

func TestReadCommand_SyncLockHeld_JSONReportsStale(t *testing.T) {
	for _, tc := range busyReadCases() {
		t.Run(tc.name, func(t *testing.T) {
			pulledProject(t, "prefix: TEST\n")
			holdFastCLIImportLock(t)
			app.jsonOutput = true
			stderr := runPreRun(t, tc.name)
			if tc.name == "agent work" {
				registerTestAgent(t, "reader")
				// Give the fixture an existing current task; missing work is an
				// independent ErrNotFound, unrelated to import ownership.
				_, err := app.store.WriteDB().ExecContext(context.Background(),
					`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`, "TEST-1", "reader")
				require.NoError(t, err)
			}
			output := captureStdout(t, func() {
				require.NoError(t, tc.run(), "busy ownership must not fail the read command")
			})
			var result map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(output), &result))
			require.JSONEq(t, "true", string(result["stale"]))
			require.JSONEq(t, `"sync_lock_busy"`, string(result["stale_reason"]))
			require.NotContains(t, stderr, "auto-import failed")
			require.Equal(t, 1, strings.Count(stderr, "possibly stale"), "JSON stdout has metadata and stderr still warns once")
			if tc.wantNode {
				require.Contains(t, output, "TEST-1", "read still returns cached node data")
			}
		})
	}
}

func TestCommandStartup_SyncLockHeld_ProtocolAndDaemonSurfacesProceed(t *testing.T) {
	for _, spelling := range []string{"serve", "mcp", "daemon", "sync daemon"} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", spelling, jsonMode), func(t *testing.T) {
				pulledProject(t, "prefix: TEST\n")
				holdFastCLIImportLock(t)
				root := newRootCmd()
				app.jsonOutput = jsonMode
				cmd, _, err := root.Find(strings.Fields(spelling))
				require.NoError(t, err)
				require.NotEqual(t, root, cmd)
				cmd.SetContext(testContext(t))
				stderr := captureStderr(t, func() {
					require.NoError(t, persistentPreRun(cmd, nil, ""), "busy ownership must not refuse startup")
				})
				node, err := app.nodeSvc.GetNode(context.Background(), "TEST-1")
				require.NoError(t, err, "startup exposes the cached services to the transport or daemon")
				require.Equal(t, "Reviewed task", node.Title)
				require.Equal(t, "sync_lock_busy", app.staleReason)
				require.Equal(t, 1, strings.Count(stderr, "possibly stale"))
				require.NotContains(t, stderr, "auto-import failed")
				// The common root hook reports on stderr before protocol/server RunE;
				// it never returns the busy sentinel to a surface's startup command.
			})
		}
	}
}

func TestDaemon_SyncLockHeld_RunsPassAndKeepsCachedData(t *testing.T) {
	for _, spelling := range []string{"daemon", "sync daemon"} {
		t.Run(spelling, func(t *testing.T) {
			pulledProject(t, "prefix: TEST\n")
			t.Setenv(transport.EnvDSN, "")
			holdFastCLIImportLock(t)
			runPreRun(t, "daemon")
			ctx, cancel := context.WithTimeout(testContext(t), 5*time.Second)
			defer cancel()
			passes := 0
			control := daemonPassControl{ticks: make(chan time.Time), observed: func(acquired bool) {
				require.True(t, acquired, "a busy sync lock does not block daemon dispatch ownership")
				passes++
				node, err := app.nodeSvc.GetNode(ctx, "TEST-1")
				require.NoError(t, err)
				require.Equal(t, "Reviewed task", node.Title)
				cancel()
			}}
			var stdout, stderr bytes.Buffer
			var err error
			if spelling == "daemon" {
				err = runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, control)
			} else {
				err = runSyncDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, true, control)
			}
			require.NoError(t, err, "native sync contention must not become a daemon error exit")
			require.Equal(t, 1, passes)
			require.Contains(t, stdout.String(), "shutting down")
		})
	}
}

func TestReadCommand_SyncLockHeld_TextPrintsOneNotice(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	holdCLIImportLock(t)
	stderr := runPreRun(t, "show")
	require.Equal(t, 1, strings.Count(stderr, "possibly stale"), stderr)
	require.Contains(t, stderr, "sync lock busy")
	require.Contains(t, stderr, "retry")
}

func TestPersistentPreRun_BusyThenFresh_ClearsFreshnessMetadata(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	lock := holdFastCLIImportLock(t)
	app.jsonOutput = true
	runPreRun(t, "show")
	require.Equal(t, "sync_lock_busy", app.staleReason)
	// Make the pulled board lossless, preserving the local annotations while
	// retaining the teammate's new task, then let the next command import it.
	local, err := app.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(".mtix", "tasks.json"))
	require.NoError(t, err)
	var incoming sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &incoming))
	incoming.Nodes[0].Annotations = local.Nodes[0].Annotations
	require.NoError(t, sqlite.RecomputeExportChecksum(&incoming))
	raw, err = json.Marshal(incoming)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(".mtix", "tasks.json"), raw, 0644))
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	require.NoError(t, app.store.Close())
	runPreRun(t, "show")
	require.Empty(t, app.staleReason)
	output := captureStdout(t, func() { require.NoError(t, runShow("TEST-2")) })
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	require.NotContains(t, result, "stale")
	require.NotContains(t, result, "stale_reason")
	require.JSONEq(t, `"Teammate task"`, string(result["title"]))
}
