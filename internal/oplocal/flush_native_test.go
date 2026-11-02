// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests cover real file handles, native delegation and publication stages.
// Injected failures do not claim hardware durability.
func flushFixture(t *testing.T) *State {
	t.Helper()
	root, err := filepath.EvalSymlinks(safeFixtureRoot(t))
	require.NoError(t, err)
	home := "/home/test"
	values := map[string]string{"TMPDIR": "/temp"}
	if runtime.GOOS == "windows" {
		home = filepath.Join("C:"+string(os.PathSeparator), "Users", "test")
		values = map[string]string{"APPDATA": filepath.Join(home, "AppData", "Roaming"), "TMPDIR": filepath.Join("C:"+string(os.PathSeparator), "Temp")}
	}
	return New(Env{GOOS: runtime.GOOS, Home: home, root: root, Values: values})
}

func flushTestFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "flush-handle-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := f.Close(); !errors.Is(err, os.ErrClosed) {
			require.NoError(t, err)
		}
	})
	return f
}

func TestWithFileHandle_LifetimeAndFailures_PreservesCauses(t *testing.T) {
	for _, tc := range []struct {
		name           string
		closed, absent bool
		callback, want error
	}{
		{"open", false, false, nil, nil},
		{"callback error", false, false, syscall.EIO, syscall.EIO},
		{"closed", true, false, nil, os.ErrClosed},
		{"nil", false, true, nil, os.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f *os.File
			if !tc.absent {
				f = flushTestFile(t)
				if tc.closed {
					require.NoError(t, f.Close())
				}
			}
			calls := 0
			err := withFileHandle(f, func(uintptr) error { calls++; return tc.callback })
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			require.Equal(t, !tc.closed && !tc.absent, calls == 1)
		})
	}
}

func TestFlushStateFile_NativeFailure_PreservesCause(t *testing.T) {
	f := flushTestFile(t)
	called := false
	err := flushStateFile(f, func(got *os.File) error { called = true; require.Same(t, f, got); return syscall.EIO })
	require.True(t, called)
	require.ErrorIs(t, err, syscall.EIO)
	require.True(t, isNativeFlushFailure(err))
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.NoError(t, flushStateFile(f, func(*os.File) error { return nil }))
}

func TestFlushStateFile_ClosedHandle_ReturnsNativeError(t *testing.T) {
	f := flushTestFile(t)
	require.NoError(t, f.Close())
	err := flushStateFile(f, nil)
	require.ErrorIs(t, err, os.ErrClosed)
	require.True(t, isNativeFlushFailure(err))
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestWriteJSON_NativeFileFlush_PreservesPreviousRecord(t *testing.T) {
	s := flushFixture(t)
	require.NoError(t, s.WriteJSON("hooks", "original"))
	before := false
	s.beforeCommit = func() error { before = true; return nil }
	s.flushNative = func(*os.File) error { return syscall.EIO }
	err := s.WriteJSON("hooks", "replacement")
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.False(t, before)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "original", got)
}

func TestCreateJSON_NativePostFlush_ReportsPublishedRecord(t *testing.T) {
	s := flushFixture(t)
	_, err := s.HostID()
	require.NoError(t, err)
	calls := 0
	s.flushNative = func(f *os.File) error {
		calls++
		if calls == 2 {
			return syscall.EIO
		}
		return platformFlush(f)
	}
	created, err := s.CreateJSON("hooks", "published")
	require.Equal(t, 2, calls)
	require.False(t, created)
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "published", got)
}

func TestHostID_NativeFlushFailure_RefusesInitialization(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"io failure", syscall.EIO}, {"already exists", os.ErrExist}} {
		t.Run(tc.name, func(t *testing.T) {
			injected := tc.err
			s := flushFixture(t)
			s.flushNative = func(*os.File) error { return injected }
			id, err := s.HostID()
			require.Empty(t, id)
			require.ErrorIs(t, err, injected)
			require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
			dir, err := s.Path()
			require.NoError(t, err)
			_, err = os.Stat(filepath.Join(dir, "host-id"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestWriteJSON_FlushAndCloseErrors_PreservesIndependentCauses(t *testing.T) {
	s := flushFixture(t)
	require.NoError(t, s.WriteJSON("hooks", "original"))
	s.flushNative = func(f *os.File) error { return errors.Join(syscall.EIO, f.Close()) }
	err := s.WriteJSON("hooks", "replacement")
	require.ErrorIs(t, err, syscall.EIO)
	require.ErrorIs(t, err, os.ErrClosed)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "original", got)
}

func TestWriteJSON_NativeFlushOptions_IsolatesConcurrentStates(t *testing.T) {
	states := []*State{flushFixture(t), flushFixture(t)}
	for _, s := range states {
		require.NoError(t, s.WriteJSON("hooks", "original"))
	}
	states[0].flushNative = func(*os.File) error { return syscall.EIO }
	gate := make(chan struct{})
	results := make([]error, 2)
	var joined sync.WaitGroup
	for i, s := range states {
		joined.Add(1)
		go func() { defer joined.Done(); <-gate; results[i] = s.WriteJSON("hooks", "replacement") }()
	}
	close(gate)
	joined.Wait()
	require.ErrorIs(t, results[0], syscall.EIO)
	require.NoError(t, results[1])
	for i, s := range states {
		var got string
		_, err := s.ReadJSON("hooks", &got)
		require.NoError(t, err)
		want := "replacement"
		if i == 0 {
			want = "original"
		}
		require.Equal(t, want, got)
	}
}
