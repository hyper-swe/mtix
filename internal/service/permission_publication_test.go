// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type permissionFaultStore struct {
	service.PermissionStore
	after bool
	fail  bool
}

func (s *permissionFaultStore) AppendAnnotationOnce(ctx context.Context, node string, annotation model.Annotation) error {
	if s.fail && !s.after {
		return errors.New("synthetic before SQLite commit")
	}
	if err := s.PermissionStore.AppendAnnotationOnce(ctx, node, annotation); err != nil {
		return err
	}
	if s.fail {
		return errors.New("synthetic after SQLite commit")
	}
	return nil
}

func permissionRoot(t *testing.T, f *wakePermissionFixture, id string) {
	t.Helper()
	require.NoError(t, f.store.CreateNode(t.Context(), &model.Node{ID: id, Project: "TEST", Title: "Synthetic permission target",
		Status: model.StatusOpen, CreatedAt: f.now, UpdatedAt: f.now, Creator: "fixture"}))
}

func TestApplyBatch_SQLitePublicationFault_TruthfulPendingAndStableRecovery(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_commit", true: "after_commit"}[after], func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			permissionRoot(t, f, "TEST-1")
			fault := &permissionFaultStore{PermissionStore: f.store, after: after, fail: true}
			f.svc.Store = fault
			batch := oplocal.PermissionBatch{ID: "0123456789abcdef0123456789abcdef", Mode: "file", Source: "fixture", Changes: []oplocal.PermissionChange{{AgentID: "first", Action: "deny"}}}
			require.Error(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
			rows, err := f.state.Confirmations(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.True(t, rows[0].Pending)
			assert.Equal(t, "pending", rows[0].BoardLine)
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Equal(t, "denied", reason)
			node, err := f.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			assert.Len(t, node.Annotations, map[bool]int{false: 0, true: 1}[after])
			fault.fail = false
			require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
			assertRecoveredPermission(t, f, batch.ID)
		})
	}
}

func assertRecoveredPermission(t *testing.T, f *wakePermissionFixture, id string) {
	t.Helper()
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assert.Equal(t, permissionAcceptedPublicationID(t, f, id), node.Annotations[0].ID)
	assert.Equal(t, service.PermissionNotice, node.Annotations[0].Text)
	assert.Equal(t, "mtix-permissions", node.Annotations[0].Author)
	assert.Empty(t, node.Annotations[0].Addressee)
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, id, rows[0].ID)
	assert.Equal(t, f.svc.HostID, rows[0].HostID)
	assert.Equal(t, f.board.ID, rows[0].BoardID)
	assert.Equal(t, "first", rows[0].AgentID)
	assert.Equal(t, "deny", rows[0].Action)
	assert.False(t, rows[0].Pending)
	assert.Equal(t, "published", rows[0].BoardLine)
}

func TestApplyBatch_ActualHookDispatcherAndAlertsReentry_BoundedAndJoined(t *testing.T) {
	if os.Getenv("MTIX_PERMISSION_REENTRY_CHILD") == "1" {
		permissionReentryChild(t)
		return
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "-test.run=^TestApplyBatch_ActualHookDispatcherAndAlertsReentry_BoundedAndJoined$", "-test.v")
	command.Env = append(os.Environ(), "MTIX_PERMISSION_REENTRY_CHILD=1")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	command.WaitDelay = 2 * time.Second
	require.NoError(t, command.Start())
	t.Logf("owned permission-reentry pid=%d", command.Process.Pid)
	err = command.Wait()
	t.Logf("joined permission-reentry pid=%d exit=%d", command.Process.Pid, command.ProcessState.ExitCode())
	require.NoError(t, ctx.Err())
	require.NoError(t, err, output.String())
}

func permissionReentryChild(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	permissionRoot(t, f, "TEST-1")
	dir := filepath.Join(f.board.Checkout, ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0700))
	writeHooks(t, dir, "hooks:\n  - name: fixture-created\n    match: {events: [node.created], to-agent: fixture-recipient}\n    deliver: [inbox]\n")
	dispatcher := service.NewHooksDispatcher(f.store, dir, slog.Default())
	f.store.AddOnCommit(dispatcher.OnCommitDispatch())
	calls := 0
	f.store.AddOnCommit(func() {
		calls++
		ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Equal(t, "denied", reason)
		rows, err := f.state.Confirmations(t.Context())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.True(t, rows[0].Pending)
		assert.Equal(t, "pending", rows[0].BoardLine)
		alerts := service.NewAlerts(f.state, func() time.Time { return f.now })
		_, err = alerts.Raise(t.Context(), service.AlertRequest{Agent: "first", Class: "wake.unconfirmed", Detail: "Synthetic callback", Fix: "mtix doctor"})
		require.NoError(t, err)
		_, err = alerts.Recent(t.Context(), 0)
		require.NoError(t, err)
	})
	f.apply(t, oplocal.PermissionChange{AgentID: "first", Action: "deny"})
	assert.Greater(t, calls, 0)
	inbox, err := f.store.InboxList(t.Context(), "fixture-recipient")
	require.NoError(t, err)
	require.Len(t, inbox, 1, "real dispatcher committed inbox delivery during publication")
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.False(t, rows[0].Pending)
	assert.Equal(t, "published", rows[0].BoardLine)
}
