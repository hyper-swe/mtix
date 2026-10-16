//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func pendingRequest(t *testing.T, f *regFixture, kind, board, path, lane, at string) sqlite.AttachPending {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"agent_id": "lane-a", "harness": "codex", "lane_path": lane})
	require.NoError(t, err)
	row := sqlite.AttachPending{Kind: kind, BoardKey: board, Path: path, Payload: string(raw), QueuedAt: at}
	row.ID, err = f.store.EnqueueAttachPending(t.Context(), row)
	require.NoError(t, err)
	return row
}

func TestDrainAttachPending_PermanentIdentityConflict_DoesNotBlockForgedRow(t *testing.T) {
	f, s, loc := identityFixture(t)
	old := "22222222-2222-4222-8222-222222222222"
	require.NoError(t, s.ListBoard(t.Context(), old, loc.Root))
	pendingRequest(t, f, "list", loc.BoardKey, loc.Root, loc.Path, "1")
	pendingRequest(t, f, "lane", old, loc.Root, loc.Path, "2")
	result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
	require.NoError(t, err)
	require.Equal(t, 2, result.Dropped)
	require.Zero(t, result.Processed)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	alerts, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.NotEmpty(t, alerts)
	var count uint64
	for _, alert := range alerts {
		count += alert.Count
	}
	require.Equal(t, uint64(2), count)
	boards, err := s.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, boards, 1)
	require.Equal(t, old, boards[0].Key)
	grants, _, err := s.BoardGrants()
	require.NoError(t, err)
	require.Empty(t, grants)
}

func TestDrainAttachPending_TransientRowFailure_ContinuesAndKeepsRetry(t *testing.T) {
	f, s, loc := identityFixture(t)
	require.NoError(t, s.WriteJSON("lanes", 123))
	retry := pendingRequest(t, f, "lane", loc.BoardKey, loc.Root, loc.Path, "1")
	pendingRequest(t, f, "lane", "22222222-2222-4222-8222-222222222222", loc.Root, loc.Path, "2")
	result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
	require.Error(t, err)
	require.Equal(t, 1, result.Dropped)
	require.Zero(t, result.Processed)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Equal(t, []sqlite.AttachPending{retry}, rows)
	alerts, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, alerts, 1)
}

func TestDrainAttachPending_AlertFailure_KeepsOnlyForgedRowForRetry(t *testing.T) {
	f, s, loc := identityFixture(t)
	require.NoError(t, s.Ensure())
	row := pendingRequest(t, f, "lane", "22222222-2222-4222-8222-222222222222", loc.Root, loc.Path, "1")
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
	require.Error(t, err)
	require.Zero(t, result.Dropped)
	require.Zero(t, result.Processed)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Equal(t, []sqlite.AttachPending{row}, rows)
}

type cancelAfterPendingDeleteStore struct {
	service.RegistrationStore
	service.AttachPendingStore
	cancel context.CancelFunc
}

func (s *cancelAfterPendingDeleteStore) DeleteAttachPending(ctx context.Context, row sqlite.AttachPending) (bool, error) {
	removed, err := s.AttachPendingStore.DeleteAttachPending(ctx, row)
	s.cancel()
	return removed, err
}

func TestDrainAttachPending_Cancellation_PreservesUnvisitedRows(t *testing.T) {
	f, s, loc := identityFixture(t)
	pendingRequest(t, f, "list", loc.BoardKey, loc.Root, loc.Path, "1")
	retry := pendingRequest(t, f, "lane", "22222222-2222-4222-8222-222222222222", loc.Root, loc.Path, "2")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.svc.Store = &cancelAfterPendingDeleteStore{RegistrationStore: f.store, AttachPendingStore: f.store, cancel: cancel}
	result, err := f.svc.DrainAttachPending(ctx, f.svc.Routing.Board)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, result.Processed)
	require.Zero(t, result.Dropped)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Equal(t, []sqlite.AttachPending{retry}, rows)
	alerts, err := s.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, alerts)
}

type pendingDeleteFaultStore struct {
	service.RegistrationStore
	service.AttachPendingStore
	faults map[int64]error
}

func (s *pendingDeleteFaultStore) DeleteAttachPending(ctx context.Context, row sqlite.AttachPending) (bool, error) {
	if err := s.faults[row.ID]; err != nil {
		return false, err
	}
	return s.AttachPendingStore.DeleteAttachPending(ctx, row)
}

func TestDrainAttachPending_MultipleDeleteFailures_JoinsCausesAndContinues(t *testing.T) {
	f, _, loc := identityFixture(t)
	first := pendingRequest(t, f, "lane", loc.BoardKey, loc.Root, loc.Path, "1")
	second := pendingRequest(t, f, "list", loc.BoardKey, loc.Root, loc.Path, "2")
	pendingRequest(t, f, "lane", "22222222-2222-4222-8222-222222222222", loc.Root, loc.Path, "3")
	firstErr, secondErr := errors.New("first delete unavailable"), errors.New("second delete unavailable")
	f.svc.Store = &pendingDeleteFaultStore{RegistrationStore: f.store, AttachPendingStore: f.store, faults: map[int64]error{first.ID: firstErr, second.ID: secondErr}}
	result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.Zero(t, result.Processed)
	require.Equal(t, 1, result.Dropped)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Equal(t, []sqlite.AttachPending{first, second}, rows)
}
