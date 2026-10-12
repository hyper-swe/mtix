// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests exercise the existing exported Acquire wrapper against real
// symlink fixtures. The compiling RED proves the legacy API must refuse
// redirected lock objects before named acquisition can safely share it.
package pushlock_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

func TestAcquire_SymlinkedLockFile_Refused(t *testing.T) {
	cases := []struct {
		name         string
		targetExists bool
	}{
		{"existing-target", true},
		{"dangling-target", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			require.NoError(t, os.Mkdir(data, 0o755))
			target := filepath.Join(t.TempDir(), "redirected-lock")
			if tc.targetExists {
				require.NoError(t, os.WriteFile(target, []byte("original target"), 0o600))
			}
			require.NoError(t, os.Symlink(target, filepath.Join(data, "sync.push.lock")),
				"real symlink fixture must be created; this test never substitutes a skip")

			lock, err := pushlock.Acquire(dir)
			if lock != nil {
				require.NoError(t, lock.Release(), "close any legacy accepted lock before refusal assertion")
			}
			require.Error(t, err, "legacy Acquire must refuse the real symlink lock file")
			require.Nil(t, lock, "refusal must never return a held redirected lock")
		})
	}
}

func TestAcquire_SymlinkedDataDir_Refused(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "data")),
		"real directory symlink fixture must be created; this test never substitutes a skip")

	lock, err := pushlock.Acquire(dir)
	if lock != nil {
		require.NoError(t, lock.Release(), "close any legacy accepted lock before refusal assertion")
	}
	require.Error(t, err, "legacy Acquire must refuse the real symlink data directory")
	require.Nil(t, lock, "refusal must never return a held lock in the redirected directory")
}

func TestAcquireNamed_SymlinkedLockFile_Refused(t *testing.T) {
	for _, name := range []string{"sync.push.lock", "dispatch.lock"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "data"), 0755))
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.WriteFile(target, []byte("unchanged"), 0600))
			require.NoError(t, os.Symlink(target, filepath.Join(dir, "data", name)))
			lock, err := pushlock.AcquireNamed(dir, name)
			require.Error(t, err)
			require.Nil(t, lock)
			bytes, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "unchanged", string(bytes))
		})
	}
}

func TestAcquireNamed_SymlinkedDataDir_Refused(t *testing.T) {
	for _, name := range []string{"sync.push.lock", "dispatch.lock"} {
		t.Run(name, func(t *testing.T) {
			dir, target := t.TempDir(), t.TempDir()
			require.NoError(t, os.Symlink(target, filepath.Join(dir, "data")))
			lock, err := pushlock.AcquireNamed(dir, name)
			require.Error(t, err)
			require.Nil(t, lock)
			entries, err := os.ReadDir(target)
			require.NoError(t, err)
			require.Empty(t, entries, "refusal must not create a redirected lock")
		})
	}
}

func TestAcquire_PushLockName_Unchanged(t *testing.T) {
	dir := t.TempDir()
	legacy, err := pushlock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, legacy.Release()) })
	named, err := pushlock.AcquireNamed(dir, "sync.push.lock")
	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	require.Nil(t, named)
	independent, err := pushlock.AcquireNamed(dir, "dispatch.lock")
	require.NoError(t, err)
	require.NoError(t, independent.Release())
	require.NoError(t, legacy.Release())
	named, err = pushlock.AcquireNamed(dir, "sync.push.lock")
	require.NoError(t, err)
	require.NoError(t, named.Release())
	require.NoError(t, named.Release())
	var empty *pushlock.PushLock
	require.NoError(t, empty.Release())
}

func TestAcquireNamed_InvalidFilename_Refused(t *testing.T) {
	cases := []string{"", ".", "..", "../outside", "dir/name", `dir\name`, "/absolute", "name:stream", "CON", "LPT1.lock", "trailing.", "white space", strings.Repeat("a", 256)}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			lock, err := pushlock.AcquireNamed(dir, name)
			require.Error(t, err)
			require.Nil(t, lock)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestAcquireNamed_DataOrFileType_Refused(t *testing.T) {
	cases := []struct {
		name       string
		dataIsFile bool
	}{{"data-file", true}, {"lock-directory", false}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			if tc.dataIsFile {
				require.NoError(t, os.WriteFile(data, []byte("data"), 0600))
			} else {
				require.NoError(t, os.Mkdir(data, 0755))
				require.NoError(t, os.Mkdir(filepath.Join(data, "dispatch.lock"), 0755))
			}
			lock, err := pushlock.AcquireNamed(dir, "dispatch.lock")
			require.Error(t, err)
			require.Nil(t, lock)
		})
	}
}
