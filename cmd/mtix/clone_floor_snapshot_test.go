// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cloneFloorOtherWriter(t *testing.T) *sqlite.Store {
	t.Helper()
	initTestApp(t)
	for range 4 {
		_, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "old history", Creator: "sender"})
		require.NoError(t, err)
	}
	other, err := sqlite.New(filepath.Join(app.mtixDir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	return other
}

func assertFreshHookAfterCloneFloor(t *testing.T, other *sqlite.Store, nodeID string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: after-discard
    match: { events: [comment.addressed], to-agent: worker }
    deliver: [inbox]
`), 0o600))
	prompts := service.NewPromptService(other, nil, slog.Default(), time.Now)
	require.NoError(t, prompts.AddAnnotation(t.Context(), nodeID, "genuinely fresh", "sender", "worker"))
	service.NewHooksDispatcher(app.store, app.mtixDir, slog.Default()).Dispatch(t.Context())
	assert.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM hook_log`), "mail appended after the floor stays hook eligible")
}

func TestCloneFloor_DiscardWhileWaitingForWriter_UsesCurrentTail(t *testing.T) {
	other := cloneFloorOtherWriter(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	held, err := app.store.WriteDB().Conn(ctx)
	require.NoError(t, err)
	release := sync.OnceFunc(func() { assert.NoError(t, held.Close()) })
	defer release()
	waits := app.store.WriteDB().Stats().WaitCount
	done := make(chan struct{})
	var stderr bytes.Buffer
	go func() { defer close(done); initCloneHookFloor(ctx, &stderr) }()
	defer func() { release(); <-done }()
	require.Eventually(t, func() bool { return app.store.WriteDB().Stats().WaitCount > waits }, 5*time.Second, time.Millisecond)
	require.NoError(t, sqlite.DiscardLocal(ctx, other, app.mtixDir))
	nodes := service.NewNodeService(other, nil, nil, slog.Default(), time.Now)
	fresh, err := nodes.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "after discard", Creator: "sender"})
	require.NoError(t, err)
	tail, err := other.JournalTail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), tail)
	release()
	<-done
	assert.Empty(t, stderr.String())
	assert.Equal(t, tail, hookFloorForClone(t), "hook floor consumes the current writer snapshot")
	assertFreshHookAfterCloneFloor(t, other, fresh.ID)
}
