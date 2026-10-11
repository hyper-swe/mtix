// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"io"
	"testing"

	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/stretchr/testify/require"
)

func TestPushHandoff_ObserverRunsAfterPassWhileOwned(t *testing.T) {
	dir := t.TempDir()
	lock, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	passReturned := false
	calls := 0
	drained, err := pushOwnedPass(io.Discard, lock, func() error {
		calls++
		require.True(t, passReturned)
		contender, heldErr := pushlock.Acquire(dir)
		require.ErrorIs(t, heldErr, pushlock.ErrLockHeld)
		require.Nil(t, contender)
		return nil
	}, func() (bool, error) { passReturned = true; return true, nil })
	require.NoError(t, err)
	require.True(t, drained)
	require.Equal(t, 1, calls)
	handoffUnlocked(t, dir)
}
func TestPushHandoff_ObserverErrorsJoinAndRelease(t *testing.T) {
	dir := t.TempDir()
	lock, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	passErr := errors.New("owned pass failure")
	observerErr := errors.New("owned observer failure")
	_, err = pushOwnedPass(io.Discard, lock, func() error { return observerErr }, func() (bool, error) { return false, passErr })
	require.ErrorIs(t, err, passErr)
	require.ErrorIs(t, err, observerErr)
	require.ErrorContains(t, err, "before releasing pushlock")
	handoffUnlocked(t, dir)
}
func TestPushHandoff_ObserverPanicStillReleases(t *testing.T) {
	dir := t.TempDir()
	lock, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	require.PanicsWithValue(t, "owned observer panic", func() {
		_, err := pushOwnedPass(io.Discard, lock, func() error { panic("owned observer panic") }, func() (bool, error) { return true, nil })
		require.NoError(t, err)
	})
	handoffUnlocked(t, dir)
}
