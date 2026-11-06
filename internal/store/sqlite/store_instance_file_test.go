// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestReadStoreInstanceIDFile_ExistingStore_ReturnsIDWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mtix.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	want, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	before, err := os.Stat(path)
	require.NoError(t, err)

	got, err := sqlite.ReadStoreInstanceIDFile(t.Context(), path)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "a read-only open leaves the file untouched")
}

func TestReadStoreInstanceIDFile_MissingOrEmptyFile_ErrorsAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.db")
	_, err := sqlite.ReadStoreInstanceIDFile(t.Context(), missing)
	require.Error(t, err)
	_, statErr := os.Stat(missing)
	assert.True(t, os.IsNotExist(statErr), "a read must never create a database")

	empty := filepath.Join(dir, "empty.db")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	_, err = sqlite.ReadStoreInstanceIDFile(t.Context(), empty)
	require.Error(t, err)
}

func TestReadStoreInstanceIDFile_NotADatabase_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.db")
	require.NoError(t, os.WriteFile(path, []byte("this is not sqlite at all, just text padding......"), 0o600))
	_, err := sqlite.ReadStoreInstanceIDFile(t.Context(), path)
	require.Error(t, err)
}
