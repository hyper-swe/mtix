// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func permissionFixtureBatch() PermissionBatch {
	return PermissionBatch{BoardID: "019a5cc9-8000-7000-8000-000000000001", Mode: "yes", Source: "agent allow", Time: alertTime(), Changes: []PermissionChange{{AgentID: "fixture-agent", Action: "allow"}}}
}
func TestPermissions_PreservesBoardGrantsAndDenies(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	board := Grant{BoardID: batch.BoardID}
	require.NoError(t, s.WriteJSON("grants", map[string]any{"board_grants": []Grant{board}, "future": "preserved"}))
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	host, err := s.HostID()
	require.NoError(t, err)
	require.True(t, grants[0].Matches(host, batch.BoardID, HubIdentity{}, "fixture-agent", false))
	require.False(t, grants[0].Matches(host, batch.BoardID, HubIdentity{}, "fixture-agent", true))
	require.False(t, grants[0].Matches("foreign", batch.BoardID, HubIdentity{}, "fixture-agent", false))
	batch.Changes[0].Action = "deny"
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err = s.AgentGrants()
	require.NoError(t, err)
	require.True(t, grants[0].Denied)
	boards, _, err := s.BoardGrants()
	require.NoError(t, err)
	require.Equal(t, []Grant{board}, boards)
	var stored map[string]any
	_, err = s.ReadJSON("grants", &stored)
	require.NoError(t, err)
	require.Equal(t, "preserved", stored["future"])
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "skipped_no_node", rows[0].BoardLine)
	path, err := s.Path()
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(path, "confirmations.jsonl"))
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}
func TestPermissions_WholeBatchValidationCreatesNothing(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.Changes = append(batch.Changes, PermissionChange{AgentID: "bad", Action: "unknown"})
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	path, err := s.Path()
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestPermissions_FaultRecoveryStablePublication(t *testing.T) {
	for _, stage := range []string{"before:permissions-pending.json", "after:permissions-pending.json", "before:confirmations.jsonl", "after:confirmations.jsonl", "before:grants.json", "after:grants.json", "before:final:confirmations.jsonl", "after:final:confirmations.jsonl", "before:permissions-clear", "after:permissions-clear"} {
		t.Run(stage, func(t *testing.T) { testPermissionRecoveryStage(t, stage) })
	}
}
func testPermissionRecoveryStage(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.ID = "11111111111111111111111111111111"
	batch.NodeID = "1"
	batch.Changes[0].Action = "deny"
	failed := false
	s.alertStage = func(at string) error {
		if at == stage && !failed {
			failed = true
			return errors.New("injected")
		}
		return nil
	}
	published := map[string]bool{}
	calls := 0
	publish := func(_ context.Context, b PermissionBatch) error {
		if !published[b.ID] {
			published[b.ID] = true
			calls++
		}
		return nil
	}
	require.Error(t, s.ApplyPermissions(t.Context(), batch, publish))
	require.True(t, failed)
	assertPermissionFaultSnapshot(t, s, batch, stage)
	restarted := New(s.env)
	require.NoError(t, restarted.ApplyPermissions(t.Context(), batch, publish))
	rows, err := restarted.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, batch.ID, rows[0].ID)
	grants, err := restarted.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)

	host, err := restarted.HostID()
	require.NoError(t, err)
	require.Equal(t, AgentGrant{HostID: host, BoardID: batch.BoardID, Hub: batch.Hub, AgentID: batch.Changes[0].AgentID, ConfirmedAt: batch.Time, Mode: batch.Mode, Source: batch.Source, Denied: true}, grants[0])
	expected := batchConfirmations(batch, host)[0]
	expected.BoardLine = "published"
	require.Equal(t, expected, rows[0])
	require.False(t, rows[0].Pending)
	require.Equal(t, 1, calls)
}
func TestPermissions_PublicationFailureRecoversPriorBatch(t *testing.T) {
	s := alertFixture(t)
	first := permissionFixtureBatch()
	first.NodeID = "old-node"
	fail := true
	published := []PermissionBatch{}
	publisher := func(_ context.Context, b PermissionBatch) error {
		if fail {
			return errors.New("unavailable")
		}
		published = append(published, b)
		return nil
	}
	require.Error(t, s.ApplyPermissions(t.Context(), first, publisher))
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Pending)
	require.Equal(t, "pending", rows[0].BoardLine)
	fail = false
	second := permissionFixtureBatch()
	second.Changes[0].AgentID = "second"
	require.NoError(t, New(s.env).ApplyPermissions(t.Context(), second, publisher))
	require.Len(t, published, 1)
	require.Equal(t, "old-node", published[0].NodeID)
	require.NotEmpty(t, published[0].ID)
	rows, err = s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.False(t, rows[0].Pending)
}
func TestPermissions_ConcurrentWritersRetainAll(t *testing.T) {
	s := alertFixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			batch := permissionFixtureBatch()
			batch.Changes[0].AgentID = fmt.Sprintf("agent-%d", i)
			failures <- New(s.env).ApplyPermissions(t.Context(), batch, nil)
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 16)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 16)
}

func permissionReentrantReadsDeny(t *testing.T) {
	t.Helper()
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.NodeID = "1"
	batch.Changes[0].Action = "deny"
	ctx, cancel := context.WithTimeout(t.Context(), 3*1e9)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.ApplyPermissions(ctx, batch, func(ctx context.Context, b PermissionBatch) error {
			_, err := s.RecordAlert(ctx, alertInput(), alertTime())
			if err != nil {
				return err
			}
			_, err = s.RecentAlerts(ctx, 0)
			if err != nil {
				return err
			}
			grants, err := s.AgentGrants()
			if err != nil {
				return err
			}
			if len(grants) != 1 || !grants[0].Denied {
				return errors.New("callback did not see deny")
			}
			rows, err := s.Confirmations(ctx)
			if err != nil {
				return err
			}
			if len(rows) != 1 || rows[0].ID != b.ID || rows[0].Action != "deny" || !rows[0].Pending || rows[0].BoardLine != "pending" {
				return errors.New("callback did not see confirmation")
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("reentrant callback deadlocked")
	}
}

func TestPermissions_SameIDChangedTargetOrHubRefused(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.ID = "11111111111111111111111111111111"
	batch.NodeID = "original"
	publisher := func(context.Context, PermissionBatch) error { return nil }
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, publisher))
	before := alertStateSnapshot(t, s)
	batch.NodeID = "different"
	require.Error(t, s.ApplyPermissions(t.Context(), batch, publisher))
	require.Equal(t, before, alertStateSnapshot(t, s))
	batch.NodeID = "original"
	batch.Hub = HubIdentity{Kind: "postgres", ProjectPrefix: "synthetic", FirstEventHash: "synthetic"}
	require.Error(t, s.ApplyPermissions(t.Context(), batch, publisher))
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func assertPermissionFaultSnapshot(t *testing.T, s *State, b PermissionBatch, stage string) {
	t.Helper()
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	if stage == "before:permissions-pending.json" {
		require.Empty(t, grants)
		require.Empty(t, rows)
		return
	}
	require.Len(t, grants, 1)
	require.True(t, grants[0].Denied)
	host, err := s.HostID()
	require.NoError(t, err)
	require.Equal(t, AgentGrant{HostID: host, BoardID: b.BoardID, Hub: b.Hub, AgentID: b.Changes[0].AgentID, ConfirmedAt: b.Time, Mode: b.Mode, Source: b.Source, Denied: true}, grants[0])
	require.Len(t, rows, 1)
	expected := batchConfirmations(b, host)[0]
	if stage == "after:permissions-clear" {
		expected.BoardLine = "published"
	} else {
		expected.Pending = true
	}
	require.Equal(t, expected, rows[0])
}
