// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func contractAcceptedPending(t *testing.T, stage string) (*State, PermissionBatch) {
	t.Helper()
	s := alertFixture(t)
	intent := replayIntent()
	s.alertStage = func(at string) error {
		if at == stage {
			return errors.New("accepted fault")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, replayPrepare, nil))
	require.NotNil(t, generationPending(t, s).Batch)
	return s, intent
}
func TestPermissions_RecoveryChangedLogPreimageRefusesThenRepairs(t *testing.T) {
	s, intent := contractAcceptedPending(t, "after:permission-replay-accepted")
	p := generationPending(t, s)
	other := permissionFixtureBatch()
	other.ID = "22222222222222222222222222222222"
	host, err := s.HostID()
	require.NoError(t, err)
	changed, err := encodeConfirmations(batchConfirmations(other, host))
	require.NoError(t, err)
	writePermissionFixture(t, s, "confirmations.jsonl", changed)
	before := alertStateSnapshot(t, s)
	require.Error(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	// Restore the original missing preimage, not a fabricated completed row.
	path, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, removeContractFile(path, "confirmations.jsonl"))
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, p.Generation, generationPending(t, s).Generation)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, intent.ID, rows[0].ID)
}
func TestPermissions_RecoveryLegacyInvalidStateNeverUpgrades(t *testing.T) {
	for _, name := range []string{"permissions-replays.json", "confirmations.jsonl", "grants.json"} {
		t.Run(name, func(t *testing.T) { testContractLegacyRepair(t, name) })
	}
}
func testContractLegacyRepair(t *testing.T, name string) {
	t.Helper()
	s, intent := contractAcceptedPending(t, "after:permission-replay-accepted")
	p := generationPending(t, s)
	p.Generation = ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	original, exists := contractReadFile(t, s, name)
	writePermissionFixture(t, s, name, []byte("{broken synthetic protected state"))
	before := alertStateSnapshot(t, s)
	require.Error(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.Empty(t, generationPending(t, s).Generation)
	if exists {
		writePermissionFixture(t, s, name, original)
	} else {
		path, err := s.Path()
		require.NoError(t, err)
		require.NoError(t, removeContractFile(path, name))
	}
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, nil))
	require.True(t, permissionID(generationPending(t, s).Generation))
	result, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, *p.Batch, result)
}
func TestPermissions_RecoveryLegacyMissingPublisherDoesNotUpgrade(t *testing.T) {
	s := alertFixture(t)
	b := permissionFixtureBatch()
	b.ID = "11111111111111111111111111111111"
	b.NodeID = "1"
	require.Error(t, s.ApplyPermissions(t.Context(), b, func(context.Context, PermissionBatch) error { return errors.New("publication unavailable") }))
	p := generationPending(t, s)
	p.Generation = ""
	require.NoError(t, s.WriteJSON("permissions-pending", p))
	before := alertStateSnapshot(t, s)
	require.ErrorContains(t, New(s.env).ApplyPermissionsScoped(t.Context(), b.BoardID, b, nil), "publisher")
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.NoError(t, New(s.env).ApplyPermissionsScoped(t.Context(), b.BoardID, b, func(context.Context, PermissionBatch) error { return nil }))
	require.True(t, permissionID(generationPending(t, s).Generation))
}

func contractReadFile(t *testing.T, s *State, name string) ([]byte, bool) {
	t.Helper()
	path, err := s.Path()
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(path, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	require.NoError(t, err)
	return data, true
}
func removeContractFile(path, name string) error { return os.Remove(filepath.Join(path, name)) }

func TestPermissions_RecoveryCancellationAfterAcceptanceRetainsCanonicalBatch(t *testing.T) {
	s := alertFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.alertStage = func(stage string) error {
		if stage == "after:permissions-pending.json" {
			cancel()
		}
		return nil
	}
	intent := replayIntent()
	require.ErrorIs(t, s.ApplyPermissionIntent(ctx, intent, replayPrepare, nil), context.Canceled)
	pending := generationPending(t, s)
	require.NotNil(t, pending.Batch)
	before := alertStateSnapshot(t, s)
	require.ErrorIs(t, New(s.env).ApplyPermissionIntent(ctx, intent, nil, nil), context.Canceled)
	require.Equal(t, before, alertStateSnapshot(t, s))
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, nil))
	result, ok, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, *pending.Batch, result)
	require.Equal(t, pending.Generation, generationPending(t, s).Generation)
}
func TestPermissions_RecoveryCorruptGrantsAfterLogProjectionPreventsPublication(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.ID = "11111111111111111111111111111111"
	batch.NodeID = "1"
	calls := 0
	publish := func(context.Context, PermissionBatch) error { calls++; return nil }
	s.alertStage = func(stage string) error {
		if stage == "after:confirmations.jsonl" {
			return errors.New("projected log fault")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissions(t.Context(), batch, publish))
	pending := generationPending(t, s)
	require.Zero(t, calls)
	writePermissionFixture(t, s, "grants.json", []byte("{broken protected grants"))
	before := alertStateSnapshot(t, s)
	require.Error(t, New(s.env).ApplyPermissions(t.Context(), batch, publish))
	require.Zero(t, calls)
	require.Equal(t, before, alertStateSnapshot(t, s))
	path, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, removeContractFile(path, "grants.json"))
	require.NoError(t, New(s.env).ApplyPermissions(t.Context(), batch, publish))
	require.Equal(t, 1, calls)
	require.Equal(t, pending.Generation, generationPending(t, s).Generation)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "published", rows[0].BoardLine)
	require.False(t, rows[0].Pending)
}
