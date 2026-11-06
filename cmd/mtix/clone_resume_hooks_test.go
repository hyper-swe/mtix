// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hookFloorForClone(t *testing.T) int64 {
	t.Helper()
	var floor int64
	require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(),
		`SELECT COALESCE(MAX(cursor),0) FROM hook_dispatch_cursor`).Scan(&floor))
	return floor
}

func TestCloneResume_PGInterruptedHistory_DoesNotFireHooks(t *testing.T) {
	nodeID := interruptedWakeCloneNode(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(`hooks:
  - name: clone-history
    match: { events: [comment.addressed], to-agent: worker }
    deliver: [inbox]
`), 0600))
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, true, 2), stderr.String())
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	assert.Equal(t, tail, hookFloorForClone(t), "resume retains the base hook floor at the cloned tail")
	dispatcher := service.NewHooksDispatcher(app.store, app.mtixDir, slog.Default())
	dispatcher.Dispatch(t.Context())
	historical := countTestRows(t, `SELECT COUNT(*) FROM hook_log`)
	assert.Zero(t, historical, "history must not fire hooks")
	assert.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM inbox_deliveries`), "no historical hook delivery")
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), nodeID, "fresh after resume", "sender", "worker"))
	dispatcher.Dispatch(t.Context())
	assert.Equal(t, historical+1, countTestRows(t, `SELECT COUNT(*) FROM hook_log`), "the hook must still fire on fresh mail")
	assert.Equal(t, historical+1, countTestRows(t, `SELECT COUNT(*) FROM inbox_deliveries`))
}
