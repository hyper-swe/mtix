// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests exercise native write-error classification through the existing
// directory callback, including content already published before a flush fails.
type injectedNativeFlushFailure struct{ cause error }

func (e injectedNativeFlushFailure) Error() string     { return e.cause.Error() }
func (e injectedNativeFlushFailure) Unwrap() error     { return e.cause }
func (injectedNativeFlushFailure) nativeFlushFailure() {}

func TestWriteJSON_FlushError_FailsWrite(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.WriteJSON("hooks", "original"))
	called := false
	s.syncDirectory = func(*os.File) error {
		called = true
		return injectedNativeFlushFailure{cause: syscall.EIO}
	}
	err := s.WriteJSON("hooks", "replacement")
	require.True(t, called)
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "replacement", got)
}

func TestCreateJSON_FlushError_PreservesPublishedRecord(t *testing.T) {
	s := fixture(t)
	_, err := s.HostID()
	require.NoError(t, err)
	called := false
	s.syncDirectory = func(*os.File) error {
		called = true
		return injectedNativeFlushFailure{cause: syscall.EIO}
	}
	created, err := s.CreateJSON("hooks", "published")
	require.True(t, called)
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.False(t, created)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "published", got)
}

func TestFailure_NativeFlush_PreservesWriteClassification(t *testing.T) {
	err := failure("record write failed", injectedNativeFlushFailure{cause: syscall.EIO})
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestWriteExecutable_NativeFileFlush_PreservesPreviousFile(t *testing.T) {
	s := fixture(t)
	path, err := s.WriteExecutable("mtix-shim", []byte("original"))
	require.NoError(t, err)
	s.flushNative = func(*os.File) error { return syscall.EIO }
	_, err = s.WriteExecutable("mtix-shim", []byte("replacement"))
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm())
}
