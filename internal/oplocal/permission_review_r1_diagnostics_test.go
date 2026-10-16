// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewR1_PermissionDiagnosticsReadOnlyMissingAndOrphanState(t *testing.T) {
	s := alertFixture(t)
	path, err := s.Path()
	require.NoError(t, err)
	host, err := s.ReadHostID()
	require.NoError(t, err)
	require.Empty(t, host)
	present, err := s.PermissionStatePresent()
	require.NoError(t, err)
	require.False(t, present)
	pending, err := s.PendingPermissionBatches(t.Context())
	require.NoError(t, err)
	require.Empty(t, pending)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, s.Ensure())
	host, err = s.ReadHostID()
	require.NoError(t, err)
	require.Empty(t, host)
	present, err = s.PermissionStatePresent()
	require.NoError(t, err)
	require.False(t, present)
	require.NoError(t, os.WriteFile(filepath.Join(path, "grants.json"), []byte("{}"), 0600))
	before := alertStateSnapshot(t, s)
	present, err = s.PermissionStatePresent()
	require.NoError(t, err)
	require.True(t, present, "orphan permission records must not look like an unconfigured operator")
	require.Equal(t, before, alertStateSnapshot(t, s))
	_, err = s.PendingPermissionBatches(t.Context())
	require.Error(t, err, "an orphan record cannot mint a replacement host identity")
	require.Equal(t, before, alertStateSnapshot(t, s))
}

func TestReviewR1_DiagnosticUnreadableStateAndCanceledReadsNeverInitialize(t *testing.T) {
	s := alertFixture(t)
	path, err := s.Path()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.PendingPermissionBatches(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.QuarantinedPermissionGrants(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Error(t, s.CancelPermissionBatch(ctx, replayIntent().ID))
	require.Error(t, s.CancelPermissionBatch(t.Context(), "invalid"))
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("synthetic non-directory"), 0600))
	_, err = s.ReadHostID()
	require.Error(t, err)
	_, err = s.PermissionStatePresent()
	require.Error(t, err)
	_, err = s.PendingPermissionBatches(t.Context())
	require.Error(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "synthetic non-directory", string(data))
}

func TestReviewR1_OrphanPresenceRefusesInvalidProtectedRecordType(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.Ensure())
	path, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(path, "permissions-pending.json"), 0700))
	_, err = s.PermissionStatePresent()
	require.Error(t, err)
	host, err := s.ReadHostID()
	require.NoError(t, err)
	require.Empty(t, host)
}
