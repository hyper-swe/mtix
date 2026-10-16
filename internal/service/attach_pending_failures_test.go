//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

type pendingFaultStore struct {
	service.RegistrationStore
	pending                        service.AttachPendingStore
	enqueueErr, listErr, deleteErr error
}

func (f *pendingFaultStore) EnqueueAttachPending(ctx context.Context, row sqlite.AttachPending) (int64, error) {
	if f.enqueueErr != nil {
		return 0, f.enqueueErr
	}
	return f.pending.EnqueueAttachPending(ctx, row)
}
func (f *pendingFaultStore) ListAttachPending(ctx context.Context) ([]sqlite.AttachPending, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.pending.ListAttachPending(ctx)
}
func (f *pendingFaultStore) DeleteAttachPending(ctx context.Context, row sqlite.AttachPending) (bool, error) {
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	return f.pending.DeleteAttachPending(ctx, row)
}

func TestRegisterSession_QueueUnavailable_ReturnsCommittedRegistrationAndCause(t *testing.T) {
	for _, kind := range []string{"error", "missing-interface"} {
		t.Run(kind, func(t *testing.T) {
			f, s, _ := identityFixture(t)
			require.NoError(t, s.Ensure())
			dir, err := s.Path()
			require.NoError(t, err)
			require.NoError(t, os.Chmod(dir, 0500))
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
			cause := errors.New("queue unavailable")
			if kind == "error" {
				f.svc.Store = &pendingFaultStore{RegistrationStore: f.store, pending: f.store, enqueueErr: cause}
			} else {
				f.svc.Store = struct{ service.RegistrationStore }{f.store}
			}
			result, err := f.svc.RegisterSession(t.Context(), req("lane-a", 1))
			require.Error(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.Registration)
			if kind == "error" {
				require.ErrorIs(t, err, cause)
			}
			rows, listErr := f.store.ListRegistrations(t.Context(), "host-a", "501")
			require.NoError(t, listErr)
			require.Len(t, rows, 1)
		})
	}
}
func TestDrainAttachPending_PersistenceFailure_KeepsRetryTruth(t *testing.T) {
	for _, kind := range []string{"list", "delete"} {
		t.Run(kind, func(t *testing.T) {
			f, _, loc := identityFixture(t)
			row := sqlite.AttachPending{Kind: "list", BoardKey: loc.BoardKey, Path: loc.Root, Payload: `{"agent_id":"lane-a","harness":"codex","lane_path":` + `"` + loc.Path + `"}`, QueuedAt: "now"}
			_, err := f.store.EnqueueAttachPending(t.Context(), row)
			require.NoError(t, err)
			cause := errors.New("persistence failed")
			fault := &pendingFaultStore{RegistrationStore: f.store, pending: f.store}
			if kind == "list" {
				fault.listErr = cause
			} else {
				fault.deleteErr = cause
			}
			f.svc.Store = fault
			result, err := f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
			require.ErrorIs(t, err, cause)
			require.Zero(t, result.Processed)
			rows, err := f.store.ListAttachPending(t.Context())
			require.NoError(t, err)
			require.Len(t, rows, 1)
			fault.listErr, fault.deleteErr = nil, nil
			result, err = f.svc.DrainAttachPending(t.Context(), f.svc.Routing.Board)
			require.NoError(t, err)
			require.Equal(t, 1, result.Processed)
		})
	}
}
func TestAttachRouting_InvalidDependencies_RefuseBeforeRegistration(t *testing.T) {
	for _, kind := range []string{"nil-state", "bad-board", "relative-board", "invalid-lane"} {
		t.Run(kind, func(t *testing.T) {
			f, _, _ := identityFixture(t)
			switch kind {
			case "nil-state":
				f.svc.Routing.State = nil
			case "bad-board":
				f.svc.Routing.Board.Key = "bad"
			case "relative-board":
				f.svc.Routing.Board.Path = "relative"
			case "invalid-lane":
				f.svc.Routing.LanePath = t.TempDir()
			}
			_, err := f.svc.RegisterSession(t.Context(), req("lane-a", 1))
			require.Error(t, err)
			rows, err := f.store.ListRegistrations(t.Context(), "host-a", "501")
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}
func TestDrainAttachPending_WrongBoardOrMissingQueue_Refuses(t *testing.T) {
	f, _, _ := identityFixture(t)
	board := f.svc.Routing.Board
	_, err := f.svc.DrainAttachPending(t.Context(), service.AttachBoard{Key: "other", Path: board.Path})
	require.Error(t, err)
	f.svc.Store = struct{ service.RegistrationStore }{f.store}
	_, err = f.svc.DrainAttachPending(t.Context(), board)
	require.Error(t, err)
	f.svc.Routing.State = nil
	_, err = f.svc.DrainAttachPending(t.Context(), board)
	require.Error(t, err)
	f.svc.Routing = nil
	_, err = f.svc.DrainAttachPending(t.Context(), board)
	require.Error(t, err)
}
