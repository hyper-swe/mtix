// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type cachedBoundaryCase struct {
	name    string
	path    string
	cliArgs []string
	run     func(io.Writer) error
	payload func(*testing.T) any
}

func cachedBoundaryCases() []cachedBoundaryCase {
	cases := []cachedBoundaryCase{
		{"agent-state", "agent state", []string{"reader"}, func(_ io.Writer) error { return runGetAgentState("reader") }, func(t *testing.T) any {
			state, err := app.agentSvc.GetAgentState(context.Background(), "reader")
			require.NoError(t, err)
			return map[string]string{"agent_id": "reader", "state": string(state)}
		}},
		{"session-summary", "session summary", []string{"reader"}, func(_ io.Writer) error { return runSessionSummary("reader") }, func(t *testing.T) any {
			summary, err := app.sessionSvc.SessionSummary(context.Background(), "reader")
			require.NoError(t, err)
			return summary
		}},
		{"repair-status-preview", "sync repair", []string{"--status"}, func(w io.Writer) error { return runSyncRepair(context.Background(), w, syncRepairFlags{status: true}) }, func(t *testing.T) any {
			report, err := app.syncSvc.RepairStatus(context.Background(), app.mtixDir, service.StatusRepairOptions{}, app.authorID)
			require.NoError(t, err)
			report.Reminder = pullFirstReminder(report)
			return report
		}},
		{"backfill-dry-run", "sync backfill", []string{"--dry-run"}, func(w io.Writer) error { return runSyncBackfill(context.Background(), w, io.Discard, true, false) }, func(t *testing.T) any {
			result, err := app.store.BackfillDryRun(context.Background())
			require.NoError(t, err)
			return map[string]any{"dry_run": true, "result": result}
		}},
	}
	for _, variant := range []struct {
		name  string
		flags reconcileFlags
	}{
		{"discard-local-default", reconcileFlags{discardLocal: true}},
		{"rename-to-explicit-dry-run", reconcileFlags{renameTo: "RENAMED", dryRun: true, yes: true}},
		{"import-as-default", reconcileFlags{importAs: "TEST-1"}},
	} {
		f := variant.flags
		cases = append(cases, cachedBoundaryCase{"reconcile-" + variant.name, "sync reconcile", cachedReconcileCLIArgs(f), func(w io.Writer) error {
			return runSyncReconcile(context.Background(), w, io.Discard, f)
		}, func(t *testing.T) any {
			plan, err := computePlan(context.Background(), app.store, f)
			require.NoError(t, err)
			return plan
		}})
	}
	return cases
}

// Use the real nested command path: a synthetic rootless "sync" command
// would exercise the auto-import exclusion instead of the preview boundary.
func cachedBoundaryPreRun(t *testing.T, path string, cliArgs ...string) string {
	t.Helper()
	root := newRootCmd()
	cmd, _, err := root.Find(strings.Fields(path))
	require.NoError(t, err)
	require.NotEqual(t, root, cmd)
	require.Equal(t, "mtix "+path, cmd.CommandPath())
	require.NoError(t, cmd.ParseFlags(cliArgs))
	cmd.SetContext(testContext(t))
	app.jsonOutput = true
	return captureStderr(t, func() { require.NoError(t, persistentPreRun(cmd, cmd.Flags().Args(), "")) })
}

func cachedBoundaryFixture(t *testing.T, busy bool) {
	t.Helper()
	pulledProject(t, "prefix: TEST\n")
	if busy {
		lock, err := os.OpenFile(filepath.Join(".mtix", "data", "sync.lock"), os.O_RDWR, 0o644)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, lock.Close()) })
		require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
		return
	}
	// Fresh control uses the exact cached export, avoiding the fixture's
	// deliberately lossy teammate board and its independent refusal path.
	store, err := sqlite.New(filepath.Join(".mtix", "data"), nil)
	require.NoError(t, err)
	board, err := store.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	raw, err := json.Marshal(board)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(".mtix", "tasks.json"), raw, 0o644))
}

func cachedBoundarySeedReaders(t *testing.T) {
	t.Helper()
	node, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	node.ID, node.Seq, node.UID, node.Title, node.ContentHash = "TEST-3", 3, "", "Cached sibling", "h-3"
	node.Annotations = nil
	require.NoError(t, app.store.CreateNode(context.Background(), node))
	_, err = app.store.WriteDB().ExecContext(context.Background(),
		`INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat) VALUES ('reader', 'TEST', 'idle', '2026-09-24T08:00:00Z', '2026-09-24T08:00:00Z')`)
	require.NoError(t, err)
	_, err = app.sessionSvc.SessionStart(context.Background(), "reader", "TEST")
	require.NoError(t, err)
}

func TestCachedReaderBoundary_BusyJSON(t *testing.T) {
	for _, tc := range cachedBoundaryCases() {
		t.Run(tc.name, func(t *testing.T) {
			cachedBoundaryFixture(t, true)
			notice := cachedBoundaryPreRun(t, tc.path, tc.cliArgs...)
			require.Equal(t, "sync_lock_busy", app.staleReason)
			require.Equal(t, 1, strings.Count(notice, "possibly stale"), notice)
			require.NotContains(t, notice, "auto-import failed")
			cachedBoundarySeedReaders(t)
			expected, err := json.Marshal(tc.payload(t))
			require.NoError(t, err)
			var report bytes.Buffer
			direct, runErr := cachedBoundaryCapture(t, func() error { return tc.run(&report) })
			require.NoError(t, runErr, "cached read remains successful")
			output := report.String()
			if tc.path == "agent state" || tc.path == "session summary" {
				output = direct
			} else {
				require.Empty(t, direct, "report uses supplied output writer")
			}
			var result map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(output), &result), output)
			t.Logf("successful cached JSON, staleReason=%s: %s", app.staleReason, output)
			stale, staleReason := result["stale"], result["stale_reason"]
			delete(result, "stale")
			delete(result, "stale_reason")
			payload, err := json.Marshal(result)
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(payload), "all original fields and values survive")
			require.NotEmpty(t, stale, "cached board reads need freshness metadata")
			require.JSONEq(t, "true", string(stale))
			require.JSONEq(t, `"sync_lock_busy"`, string(staleReason))
		})
	}
}

func TestCachedReaderBoundary_FreshJSONShape(t *testing.T) {
	for _, tc := range cachedBoundaryCases() {
		t.Run(tc.name, func(t *testing.T) {
			cachedBoundaryFixture(t, false)
			notice := cachedBoundaryPreRun(t, tc.path, tc.cliArgs...)
			require.Empty(t, app.staleReason)
			require.NotContains(t, notice, "possibly stale")
			cachedBoundarySeedReaders(t)
			expected, err := json.Marshal(tc.payload(t))
			require.NoError(t, err)
			var report bytes.Buffer
			direct, runErr := cachedBoundaryCapture(t, func() error { return tc.run(&report) })
			require.NoError(t, runErr)
			output := report.String()
			if tc.path == "agent state" || tc.path == "session summary" {
				output = direct
			} else {
				require.Empty(t, direct)
			}
			require.JSONEq(t, string(expected), output, "fresh responses retain the original unwrapped schema")
		})
	}
}

type cachedBoundaryFailWriter struct {
	err    error
	writes int
}

func (w *cachedBoundaryFailWriter) Write(_ []byte) (int, error) { w.writes++; return 0, w.err }

// Exercise existing writer-aware command APIs, so this regression compiles
// before any shared io.Writer factory is introduced by the corrective patch.
func TestCachedReaderBoundary_ReportWriterErrors(t *testing.T) {
	for _, tc := range cachedBoundaryCases() {
		if tc.path == "agent state" || tc.path == "session summary" {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			cachedBoundaryFixture(t, false)
			cachedBoundaryPreRun(t, tc.path, tc.cliArgs...)
			app.staleReason = "sync_lock_busy"
			sentinel := errors.New("report output unavailable")
			writer := &cachedBoundaryFailWriter{err: sentinel}
			direct, runErr := cachedBoundaryCapture(t, func() error { return tc.run(writer) })
			require.ErrorIs(t, runErr, sentinel)
			require.Empty(t, direct, "routing must retain supplied output writer")
			require.Greater(t, writer.writes, 0)
		})
	}
}

// The public factory snapshots freshness and propagates failure from its
// existing stdout writer. Reports must preserve the same error contract when
// their supplied io.Writer is routed through the shared implementation.
func TestCachedReaderBoundary_SharedFactoryWriterError(t *testing.T) {
	saveAndResetApp(t)
	app.staleReason = "sync_lock_busy"
	read, write, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, read.Close())
	require.NoError(t, write.Close())
	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original }()
	writer := NewOutputWriter(true)
	require.ErrorIs(t, writer.WriteJSON(map[string]string{"agent_id": "reader", "state": "idle"}), os.ErrClosed)
}

func cachedBoundaryCapture(t *testing.T, run func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	require.NoError(t, err)
	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original; _ = write.Close(); _ = read.Close() }()
	var output bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&output, read); close(done) }()
	runErr := run()
	require.NoError(t, write.Close())
	<-done
	return output.String(), runErr
}

// Applied maintenance reports acknowledge committed work rather than a cached preview.
func TestCachedReaderBoundary_MutationAcknowledgementsKeepShape(t *testing.T) {
	saveAndResetApp(t)
	app.jsonOutput = true
	app.staleReason = "sync_lock_busy"
	t.Run("repair apply", func(t *testing.T) {
		report := &service.StatusRepairReport{Apply: true}
		expected, err := json.Marshal(report)
		require.NoError(t, err)
		var output bytes.Buffer
		require.NoError(t, writeStatusRepairReport(&output, report))
		require.JSONEq(t, string(expected), output.String())
	})
	t.Run("backfill applied", func(t *testing.T) {
		result := sqlite.BackfillResult{NodeCount: 2}
		expected, err := json.Marshal(map[string]any{"dry_run": false, "result": result})
		require.NoError(t, err)
		var output bytes.Buffer
		require.NoError(t, printBackfillResult(&output, result, false))
		require.JSONEq(t, string(expected), output.String())
	})
}

func TestCachedReaderBoundary_BackfillAppliedWriterError(t *testing.T) {
	cachedBoundaryFixture(t, false)
	cachedBoundaryPreRun(t, "sync backfill")
	app.staleReason = "sync_lock_busy"
	sentinel := errors.New("applied backfill output unavailable")
	writer := &cachedBoundaryFailWriter{err: sentinel}
	require.ErrorIs(t, runSyncBackfill(t.Context(), writer, io.Discard, false, true), sentinel)
	require.Greater(t, writer.writes, 0)
}

func TestCachedReaderBoundary_BackfillHumanWriterError(t *testing.T) {
	saveAndResetApp(t)
	sentinel := errors.New("backfill text output unavailable")
	require.ErrorIs(t, printBackfillResult(&cachedBoundaryFailWriter{err: sentinel}, sqlite.BackfillResult{}, true), sentinel)
}

// Keep the real command flags aligned with the report handler under test.
func cachedReconcileCLIArgs(f reconcileFlags) []string {
	var args []string
	if f.discardLocal {
		args = append(args, "--discard-local")
	}
	if f.renameTo != "" {
		args = append(args, "--rename-to", f.renameTo)
	}
	if f.importAs != "" {
		args = append(args, "--import-as", f.importAs)
	}
	if f.dryRun {
		args = append(args, "--dry-run")
	}
	if f.yes {
		args = append(args, "--yes")
	}
	return args
}
