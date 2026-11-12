// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// Real enrolment keeps this lock outside data so it survives moving the store.
func holdGlueEnrolLock(t *testing.T, root string) {
	t.Helper()
	lock, err := pushlock.AcquireNamed(filepath.Join(root, ".mtix", "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
}

func glueEnrolMarker(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, service.WriteEnrolMarker(filepath.Join(root, ".mtix"), service.EnrolMarker{
		Aside: filepath.Join(root, ".mtix", "data.pre-enrol-test"), Backup: "retained-backup", Started: "2026-11-07T00:00:00Z", PID: 4242,
	}))
}

func TestProductionGlueDrain_EnrolGuard_DoesNotCreateStore(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    error
	}{
		{"running", holdGlueEnrolLock, service.ErrEnrolInProgress},
		{"interrupted", glueEnrolMarker, service.ErrEnrolInterrupted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testutil.OperatorState(t)
			root := t.TempDir()
			tt.prepare(t, root)
			err := productionGlueDrain(t.Context(), attach.GlueDecision{Act: true, BoardRoot: root, StoreRoot: root, BoardKey: "matched-board"})
			assert.ErrorIs(t, err, tt.want)
			_, statErr := os.Stat(filepath.Join(root, ".mtix", "data"))
			assert.True(t, errors.Is(statErr, os.ErrNotExist), "guard must run before sqlite.New can create data")
		})
	}
}

func TestGlue_SessionStart_EnrolGuard_LeavesPendingQueue(t *testing.T) {
	for _, tt := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{"running", holdGlueEnrolLock, "worktree enrolment in progress"},
		{"interrupted", glueEnrolMarker, "worktree enrolment was interrupted"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			root := listedBoardCheckout(t, st)
			boards, err := st.DispatchBoards(t.Context())
			require.NoError(t, err)
			enqueueRow(t, root, forgedRow(t, boards.Listed[0].Key, t.TempDir()))
			db := filepath.Join(root, ".mtix", "data", "mtix.db")
			before, err := os.ReadFile(db)
			require.NoError(t, err)
			tt.prepare(t, root)
			t.Chdir(root)
			adapter := &recordingAdapter{}
			stdout, stderr, err := runAttachVerbForTest(t, productionGlueDeps(st, adapter), "session-start", "--harness", "claude-code", "--protocol", "2")
			require.NoError(t, err, "the harness hook remains fail-soft")
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tt.want)
			after, err := os.ReadFile(db)
			require.NoError(t, err)
			assert.Equal(t, before, after, "the guarded drain must leave store bytes unchanged")
			assert.Len(t, pendingRows(t, root), 1)
			assert.Len(t, adapter.calls, 1)
			alerts := attachAlerts(t, st)
			require.Len(t, alerts, 1)
			assert.Contains(t, alerts[0].Detail, tt.want)
		})
	}
}
