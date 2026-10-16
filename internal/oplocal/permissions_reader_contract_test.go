// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissions_ReadersMissingCanceledAndInvalidLookup(t *testing.T) {
	s := alertFixture(t)
	path, err := s.Path()
	require.NoError(t, err)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
	_, ok, err := s.PermissionReplayResult(t.Context(), replayIntent().BoardID, replayIntent().ID)
	require.NoError(t, err)
	require.False(t, ok)
	usage, err := s.PermissionReplayUsage(t.Context())
	require.NoError(t, err)
	require.Zero(t, usage.Count)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = s.PermissionReplayResult(ctx, replayIntent().BoardID, replayIntent().ID)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.PermissionReplayUsage(ctx)
	require.ErrorIs(t, err, context.Canceled)
	_, _, err = s.PermissionReplayResult(t.Context(), "bad", replayIntent().ID)
	require.Error(t, err)
	_, _, err = s.PermissionReplayResult(t.Context(), replayIntent().BoardID, "bad")
	require.Error(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}
func TestPermissions_ReadersCorruptProtectedStateNeverReportAbsent(t *testing.T) {
	for _, name := range []string{"permissions-pending.json", "permissions-replays.json"} {
		t.Run(name, func(t *testing.T) {
			s := alertFixture(t)
			require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
			writePermissionFixture(t, s, name, []byte(`{"sentinel_private_fixture":"not-json"`))
			before := alertStateSnapshot(t, s)
			_, _, resultErr := s.PermissionReplayResult(t.Context(), replayIntent().BoardID, replayIntent().ID)
			_, usageErr := s.PermissionReplayUsage(t.Context())
			_, grantsErr := s.AgentGrants()
			_, rowsErr := s.Confirmations(t.Context())
			for _, err := range []error{resultErr, usageErr, grantsErr, rowsErr} {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "sentinel_private_fixture")
			}
			require.Equal(t, before, alertStateSnapshot(t, s))
		})
	}
}
func TestPermissions_ReadersProtectedPendingSymlinkRefused(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("native symlink fixture")
	}
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissionIntent(t.Context(), replayIntent(), replayPrepare, nil))
	path, err := s.Path()
	require.NoError(t, err)
	name := filepath.Join(path, "permissions-pending.json")
	require.NoError(t, os.Rename(name, name+".saved"))
	require.NoError(t, os.Symlink(name+".saved", name))
	_, _, err = s.PermissionReplayResult(t.Context(), replayIntent().BoardID, replayIntent().ID)
	require.Error(t, err)
	_, err = s.PermissionReplayUsage(t.Context())
	require.Error(t, err)
	_, err = s.AgentGrants()
	require.Error(t, err)
}
func TestPermissions_ReadersBoundRepeatedActualAcceptedWrites(t *testing.T) {
	s := alertFixture(t)
	require.NoError(t, s.ApplyPermissions(t.Context(), permissionFixtureBatch(), nil))
	writes := 0
	s.permissionReadStage = func(stage string) {
		require.Equal(t, "after:grants", stage)
		writes++
		b := permissionFixtureBatch()
		b.ID = fmt.Sprintf("%032x", writes)
		b.Changes[0].AgentID = fmt.Sprintf("writer-%d", writes)
		require.NoError(t, New(s.env).ApplyPermissions(t.Context(), b, nil))
	}
	_, err := s.Confirmations(t.Context())
	require.ErrorContains(t, err, "changed repeatedly")
	require.Equal(t, 4, writes)
	s.permissionReadStage = nil
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 5)
}
