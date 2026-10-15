// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type wakeCompatRun struct {
	entered    chan model.WakeAttempt
	release    chan struct{}
	result     chan error
	done       chan struct{}
	dispatcher *service.WakeDispatcher
}

func startWakeCompatSender(t *testing.T, f *wakeCompatFixture) *wakeCompatRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	run := &wakeCompatRun{entered: make(chan model.WakeAttempt, 1), release: make(chan struct{}), result: make(chan error, 1), done: make(chan struct{})}
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: f.store, Alerts: f.alerts,
		Send: func(ctx context.Context, attempt model.WakeAttempt) error {
			f.sent.Add(1)
			run.entered <- attempt
			select {
			case <-run.release:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("compatibility sender cancelled: %w", ctx.Err())
			}
		}})
	require.NoError(t, err)
	run.dispatcher = d
	go func() {
		defer close(run.done)
		run.result <- d.Pass(ctx, []model.WakeKey{f.key})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-run.done:
		case <-time.After(6 * time.Second):
			t.Error("owned compatibility sender did not join")
		}
	})
	return run
}

func runWakeCompatOldMutation(t *testing.T, binary, project string) []byte {
	t.Helper()
	raw, err := os.ReadFile(binary)
	require.NoError(t, err)
	t.Logf("released dispatch witness commit=%s sha256=%x", compat054TagCommit, sha256.Sum256(raw))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "create", "released dispatch witness")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "MTIX_SYNC_DSN=", "MTIX_SYNC_HOOK=", "MTIX_AUTHOR_ID=test-author")
	output, err := cmd.CombinedOutput()
	if cmd.ProcessState != nil {
		t.Logf("released dispatch witness joined pid=%d exit=%d", cmd.Process.Pid, cmd.ProcessState.ExitCode())
	}
	require.NoError(t, err, "released dispatch witness: %s", output)
	require.NoError(t, ctx.Err())
	return output
}

func assertWakeCompatLegacyPass(t *testing.T, f *wakeCompatFixture) {
	t.Helper()
	var remaining int
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM hook_dispatch_ledger WHERE hook_name=? OR hook_name=?", "wake-compat-stale", "wake-compat-prune").Scan(&remaining))
	require.Zero(t, remaining, "the released prune must remove both terminal hook rows")
	entries, err := f.store.ReadHookLog(t.Context(), 100)
	require.NoError(t, err)
	found := false
	for _, entry := range entries {
		if entry.Hook == "wake-compat-stale" && entry.Outcome == "error" && entry.Detail == "stale claim abandoned" {
			found = true
		}
	}
	require.True(t, found, "the real stale sweep must have run, not just the prune")
	floor, err := f.store.HookCursor(t.Context())
	require.NoError(t, err)
	require.Greater(t, floor, f.key.EventSeq, "the real dispatch must scan the new mutation past the pending wake")
}
