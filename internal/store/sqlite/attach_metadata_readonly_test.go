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
