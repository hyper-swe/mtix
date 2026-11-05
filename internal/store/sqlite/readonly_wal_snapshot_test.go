// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// walSnapshot is a copy of a live WAL database: the main file plus its -wal,
// taken while the writer is still open, so the committed row lives only in the
// -wal. A read-write open of this copy would checkpoint it and delete the
// -wal on close; a read-only open must not touch either file.
type walSnapshot struct {
	dir, db, wal string
	dbBytes      []byte
	dbMtime      time.Time
	walBytes     []byte
	walMtime     time.Time
}

func newWALSnapshot(t *testing.T) walSnapshot {
	t.Helper()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "mtix.db")
	s, err := New(src, slog.Default())
	require.NoError(t, err)
	insertOneNode(t, s, "TEST-1")

	_, dir := specialDir(t, "a?b")
	snap := walSnapshot{dir: dir, db: filepath.Join(dir, "mtix.db")}
	snap.wal = snap.db + "-wal"
	for _, suffix := range []string{"", "-wal"} {
		b, rerr := os.ReadFile(src + suffix) //nolint:gosec // G304: a test-owned temp path.
		require.NoError(t, rerr, "the live store must have a %q file", suffix)
		require.NoError(t, os.WriteFile(snap.db+suffix, b, 0o600))
	}
	require.NoError(t, s.Close())

	snap.dbBytes, snap.dbMtime = readWithMtime(t, snap.db)
	snap.walBytes, snap.walMtime = readWithMtime(t, snap.wal)
	require.NotEmpty(t, snap.walBytes, "the committed row must live in the -wal")
	return snap
}

func readWithMtime(t *testing.T, path string) ([]byte, time.Time) {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a test-owned temp path.
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	return b, info.ModTime()
}

// requireUntouched asserts the snapshot's main file and -wal are byte- and
// mtime-identical and that nothing but the allowed -shm appeared.
func (w walSnapshot) requireUntouched(t *testing.T) {
	t.Helper()
	b, mt := readWithMtime(t, w.db)
	assert.Equal(t, w.dbBytes, b, "the main file must not change")
	assert.True(t, w.dbMtime.Equal(mt), "the main file mtime must not change")
	b, mt = readWithMtime(t, w.wal)
	assert.Equal(t, w.walBytes, b, "the -wal must survive unchanged (a read-write close deletes it)")
	assert.True(t, w.walMtime.Equal(mt), "the -wal mtime must not change")
	for _, name := range visibleEntries(t, w.dir) {
		assert.Contains(t, []string{"mtix.db", "mtix.db-wal", "mtix.db-shm"}, name, "no other file may appear")
	}
}

// TestSalvageFromDB_WALSnapshot_LeavesFilesUntouched pins the read-only intent
// of recover (MTIX-112.2.3): dropping mode=ro makes the open read-write.
func TestSalvageFromDB_WALSnapshot_LeavesFilesUntouched(t *testing.T) {
	snap := newWALSnapshot(t)
	res := &RecoverResult{}
	out, err := salvageFromDB(context.Background(), snap.db, res, nil)
	require.NoError(t, err)
	assert.Contains(t, out.nodes, "TEST-1")
	snap.requireUntouched(t)
}

// TestVerifyDatabase_WALSnapshot_LeavesFilesUntouched pins the read-only
// intent of backup verify: verification must never write its subject.
func TestVerifyDatabase_WALSnapshot_LeavesFilesUntouched(t *testing.T) {
	snap := newWALSnapshot(t)
	ok, err := verifyDatabase(context.Background(), snap.db)
	require.NoError(t, err)
	assert.True(t, ok)
	snap.requireUntouched(t)
}
