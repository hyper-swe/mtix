// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify independent approval decisions during hook dispatch.
package service_test

import (
	"context"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestHooksDispatch_OneHookEdited_OtherHookDelivered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a Unix interpreter")
	}
	svc, store, _ := newTestNodeService(t)
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	scriptA := filepath.Join(root, "a.sh")
	scriptB := filepath.Join(root, "b.sh")
	require.NoError(t, os.WriteFile(scriptA, []byte("#!/bin/sh\nexit 0\n"), 0700))
	require.NoError(t, os.WriteFile(scriptB, []byte("#!/bin/sh\nexit 0\n"), 0700))
	writeHooks(t, dir, `hooks:
  - name: hook-a
    match: {events: [status.changed], status-to: [done]}
    deliver: [exec]
    exec: {command: [/bin/sh, "`+scriptA+`"]}
  - name: hook-b
    match: {events: [status.changed], status-to: [done]}
    deliver: [exec]
    exec: {command: [/bin/sh, "`+scriptB+`"]}
`)
	approveTestHooks(t, dir)
	require.NoError(t, os.WriteFile(scriptA, []byte("#!/bin/sh\nexit 1\n"), 0700))
	makeDoneEvent(t, svc)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(context.Background())
	assert.Zero(t, deliveredCount(t, store, "hook-a"))
	assert.Equal(t, 1, deliveredCount(t, store, "hook-b"))
}

func approvalDispatchFixture(t *testing.T) (*service.NodeService, *sqlite.Store, string, string) {
	t.Helper()
	svc, store, _ := newTestNodeService(t)
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	scriptA := filepath.Join(root, "a.sh")
	scriptB := filepath.Join(root, "b.sh")
	require.NoError(t, os.WriteFile(scriptA, []byte("#!/bin/sh\nexit 0\n"), 0700))
	require.NoError(t, os.WriteFile(scriptB, []byte("#!/bin/sh\nexit 0\n"), 0700))
	writeHooks(t, dir, `hooks:
  - name: hook-a
    match: {events: [status.changed], status-to: [done]}
    deliver: [exec]
    exec: {command: [/bin/sh, "`+scriptA+`"]}
  - name: hook-b
    match: {events: [status.changed], status-to: [done]}
    deliver: [exec]
    exec: {command: [/bin/sh, "`+scriptB+`"]}
`)
	approveTestHooks(t, dir)
	return svc, store, dir, scriptA
}

func TestHooksDispatch_HookPathInput_OtherHookDelivered(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a Unix interpreter")
	}
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "recovery"}[recovery], func(t *testing.T) {
			svc, store, dir, scriptA := approvalDispatchFixture(t)
			replacement := filepath.Join(t.TempDir(), "replacement.sh")
			require.NoError(t, os.WriteFile(replacement, []byte("#!/bin/sh\nexit 0\n"), 0700))
			require.NoError(t, os.Remove(scriptA))
			require.NoError(t, os.Symlink(replacement, scriptA))
			makeDoneEvent(t, svc)
			if recovery {
				prepareApprovalRecovery(t, store)
			}
			service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(context.Background())
			assert.Zero(t, deliveredCount(t, store, "hook-a"))
			assert.Equal(t, 1, deliveredCount(t, store, "hook-b"))
		})
	}
}

func prepareApprovalRecovery(t *testing.T, store *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	tail, err := store.JournalTail(ctx)
	require.NoError(t, err)
	for _, name := range []string{"hook-a", "hook-b"} {
		won, err := store.ClaimHookDispatch(ctx, name, tail, time.Minute)
		require.NoError(t, err)
		require.True(t, won)
	}
	_, err = store.WriteDB().Exec(`UPDATE hook_dispatch_ledger SET fired_at = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	require.NoError(t, err)
	_, err = store.WriteDB().Exec(`INSERT INTO hook_dispatch_cursor (id,cursor) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET cursor=excluded.cursor`, tail)
	require.NoError(t, err)
}
