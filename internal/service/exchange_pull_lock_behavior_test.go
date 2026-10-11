// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pull ownership exposes only native contention and preserves prior provenance.
package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExchangePullLock_BusyIdentityAndPriorState_Preserved(t *testing.T) {
	s, dir := exchangeFixture(t)
	prior := exchangePublishReference(t, s, dir)
	require.NoError(t, s.saveExchangeReason(t.Context(), "publish-failed"))
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	contender, err := s.BeginExchangePull(t.Context(), dir)
	require.Nil(t, contender)
	wrapped := fmt.Errorf("owned caller: %w", err)
	require.ErrorIs(t, wrapped, ErrNoExchangeRecord)
	require.ErrorIs(t, wrapped, ErrExchangeLockBusy)
	require.NotContains(t, err.Error(), dir)
	require.Contains(t, err.Error(), "pull not performed")
	require.Contains(t, err.Error(), "retry after the current owner finishes")
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
	owner.Close()
	got, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, prior, got)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "publish-failed", status.Reason)
	next, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	next.Close()
}

func TestExchangePullLock_FilesystemAndCancelledContext_NotBusy(t *testing.T) {
	s, _ := exchangeFixture(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data"), []byte("owned obstruction"), 0600))
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.Nil(t, owner)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	require.NotErrorIs(t, err, ErrExchangeLockBusy)
	require.NotContains(t, err.Error(), dir)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	owner, err = s.BeginExchangePull(ctx, dir)
	require.Nil(t, owner)
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrExchangeLockBusy)
	raw, err := os.ReadFile(filepath.Join(dir, "data"))
	require.NoError(t, err)
	require.Equal(t, "owned obstruction", string(raw))
}
