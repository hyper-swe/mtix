// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The read the glue predicate makes can neither write nor create: that is what
// "the predicate reads only" rests on.
func TestOpenStoreReadOnly_RefusesWritesOnAnExistingStore(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())

	db, err := openStoreReadOnly(filepath.Join(dir, "mtix.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), "INSERT INTO meta(key,value) VALUES('glue-probe','x')")
	require.Error(t, err, "a write through the read-only open must fail")
	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM meta WHERE key='glue-probe'").Scan(&n))
	assert.Zero(t, n)
}

func TestOpenStoreReadOnly_MissingFile_IsNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	db, err := openStoreReadOnly(path)
	if err == nil {
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		_, err = db.ExecContext(t.Context(), "CREATE TABLE probe(x)")
	}
	require.Error(t, err)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "opening for a read must not create a database")
}

// dirListing names every entry in dir, so a test can prove a read added none.
func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A store that was closed cleanly has no side files; reading its identity
// must not leave any (a decline writes nothing, MTIX-112.19.8).
func TestReadStoreInstanceIDFile_ClosedStore_LeavesNoSideFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	want, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	before := dirListing(t, dir)
	require.NotContains(t, before, "mtix.db-wal", "fixture: a closed store has no WAL")

	got, err := ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, before, dirListing(t, dir), "the read added a file next to the database")
}

// While another process holds the store open its WAL and shm exist; the read
// sees them and still adds nothing.
func TestReadStoreInstanceIDFile_StoreInUse_ReadsThroughExistingWALAndAddsNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	want, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	before := dirListing(t, dir)

	got, err := ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, before, dirListing(t, dir))
}

// A WAL left without its shm cannot be read without creating the shm, so the
// read refuses (the caller declines) rather than writing.
func TestReadStoreInstanceIDFile_WALWithoutShm_RefusesAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mtix.db-wal"), []byte("stale"), 0o600))
	before := dirListing(t, dir)

	_, err = ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
	require.Error(t, err)
	assert.Equal(t, before, dirListing(t, dir))
}

func TestReadStoreInstanceIDFile_ShmWithoutWAL_RefusesAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mtix.db-shm"), make([]byte, 32768), 0o600))
	before := dirListing(t, dir)

	_, err = ReadStoreInstanceIDFile(t.Context(), filepath.Join(dir, "mtix.db"))
	require.Error(t, err)
	assert.Equal(t, before, dirListing(t, dir))
}
