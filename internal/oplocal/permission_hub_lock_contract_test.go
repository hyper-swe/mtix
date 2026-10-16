// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPermissions_HubTransitionCancelContentionAndRelease(t *testing.T) {
	s := alertFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.WithHubTransition(ctx, func() error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return errors.New("observer failed")
		})
	}()
	defer func() {
		close(release)
		cancel()
		select {
		case err := <-done:
			require.ErrorContains(t, err, "observer failed")
		case <-time.After(time.Second):
			t.Error("holder did not join after release and cancel")
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("holder did not enter")
	}
	waiting, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	calls := 0
	err := New(s.env).WithHubTransition(waiting, func() error { calls++; return nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, calls)
}
func TestPermissions_HubTransitionErrorReleasesAndSafeReentry(t *testing.T) {
	s := alertFixture(t)
	boom := errors.New("observer failure")
	require.ErrorIs(t, s.WithHubTransition(t.Context(), func() error { return boom }), boom)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- New(s.env).WithHubTransition(ctx, func() error {
			if err := s.ApplyPermissions(ctx, permissionFixtureBatch(), nil); err != nil {
				return err
			}
			if _, err := s.RecordAlert(ctx, alertInput(), alertTime()); err != nil {
				return err
			}
			_, err := s.Confirmations(ctx)
			return err
		})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		cancel()
		select {
		case <-done:
			t.Fatal("observer exceeded deadline; joined after cancellation")
		case <-time.After(time.Second):
			t.Fatal("observer did not join after cancellation")
		}
	}
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
}
func TestPermissions_HubTransitionCanceledAndUnsafeLockSuppressObserver(t *testing.T) {
	s := alertFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	observe := func() error { calls++; return nil }
	require.ErrorIs(t, s.WithHubTransition(ctx, observe), context.Canceled)
	missingPath, err := s.Path()
	require.NoError(t, err)
	_, err = os.Stat(missingPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, s.WithHubTransition(t.Context(), observe))
	path, err := s.Path()
	require.NoError(t, err)
	lock := filepath.Join(path, "hub-permissions.lock")
	require.NoError(t, os.Remove(lock))
	require.NoError(t, os.Mkdir(lock, 0700))
	before := calls
	require.Error(t, s.WithHubTransition(t.Context(), observe))
	require.Equal(t, before, calls)
}
