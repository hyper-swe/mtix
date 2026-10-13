// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify that exec launches are prepared and audited by dispatch.
package service_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func launchPlanFixture(t *testing.T, command string) (*sqlite.Store, string, *service.NodeService) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a Unix interpreter")
	}
	svc, store, _ := newTestNodeService(t)
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	writeHooks(t, dir, `hooks:
  - name: hook-a
    match: {events: [status.changed], status-to: [done]}
    deliver: [exec]
    exec: {command: `+command+`}
`)
	approveTestHooks(t, dir)
	makeDoneEvent(t, svc)
	return store, dir, svc
}

func hookLogFor(t *testing.T, store *sqlite.Store, hook, outcome string) []sqlite.HookLogEntry {
	t.Helper()
	entries, err := store.ReadHookLog(context.Background(), 1000)
	require.NoError(t, err)
	var out []sqlite.HookLogEntry
	for _, e := range entries {
		if e.Hook == hook && e.Outcome == outcome {
			out = append(out, e)
		}
	}
	return out
}

func TestHooksDispatch_BareNameRecordedAsAbsolute(t *testing.T) {
	store, dir, _ := launchPlanFixture(t, "[sh, a.sh]")
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(context.Background())
	want, err := exec.LookPath("sh")
	require.NoError(t, err)
	delivered := hookLogFor(t, store, "hook-a", "delivered")
	require.Len(t, delivered, 1)
	require.Contains(t, delivered[0].Detail, "sh="+want)
}

func TestHooksDispatch_UnpreparableLaunchIsErrorOutcome(t *testing.T) {
	store, dir, _ := launchPlanFixture(t, "[no-such-launcher-xyz, a.sh]")
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(context.Background())
	require.Zero(t, deliveredCount(t, store, "hook-a"))
	require.Len(t, hookLogFor(t, store, "hook-a", "error"), 1)
}
