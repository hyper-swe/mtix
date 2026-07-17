// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-58: on an unsafe filesystem, mtix opens READ-ONLY (so recover/query/export
// still work) and HARD-REFUSES every write — with NO environment override. Both
// field corruptions were MTIX_ALLOW_UNSAFE_FS override writes on FUSE; a warning
// that can be overridden is a scheduled incident. The non-WAL "safe mode" write
// path is retired.

// initSafeStore creates a fully-initialized store on dir over a (default, local)
// filesystem, then closes it — leaving a valid schema to reopen read-only.
func initSafeStore(t *testing.T, dir string) {
	t.Helper()
	s, err := New(dir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(logSink{}, nil)) }

// TestStoreNew_UnsafeFS_OpensReadOnly_RefusesWrites: the core contract.
func TestStoreNew_UnsafeFS_OpensReadOnly_RefusesWrites(t *testing.T) {
	dir := t.TempDir()
	initSafeStore(t, dir) // schema written on a local FS

	injectFS(t, "macfuse", fsFuse) // now the FS looks like FUSE
	s, err := New(dir, quietLogger())
	require.NoError(t, err, "unsafe FS opens READ-ONLY (for recovery/query), not a hard open-refusal")
	t.Cleanup(func() { _ = s.Close() })

	// Reads work.
	var n int
	require.NoError(t, s.ReadDB().QueryRowContext(context.Background(),
		"SELECT count(*) FROM nodes").Scan(&n))
	assert.Equal(t, 0, n)

	// Writes are refused with an actionable, matchable sentinel that names the FS.
	werr := s.WithTx(context.Background(), func(_ *sql.Tx) error { return nil })
	require.Error(t, werr, "a write on an unsafe FS must be refused")
	assert.ErrorIs(t, werr, errUnsafeFilesystem)
	assert.Contains(t, werr.Error(), "macfuse", "the refusal names the filesystem")
}

// TestStoreNew_UnsafeFS_NoEnvReenablesWrites: the retired override.
func TestStoreNew_UnsafeFS_NoEnvReenablesWrites(t *testing.T) {
	dir := t.TempDir()
	initSafeStore(t, dir)
	injectFS(t, "nfs", fsNetwork)
	t.Setenv(allowUnsafeFSEnv, "1") // the RETIRED write override

	s, err := New(dir, quietLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	werr := s.WithTx(context.Background(), func(_ *sql.Tx) error { return nil })
	require.ErrorIs(t, werr, errUnsafeFilesystem,
		"MTIX_ALLOW_UNSAFE_FS must NOT re-enable writes on an unsafe FS")
}

// TestStoreNew_UnsafeFS_DirectWriteAlsoRejected: even a write that bypasses
// WithTx (a read-only connection) cannot mutate — belt-and-suspenders.
func TestStoreNew_UnsafeFS_DirectWriteRejected(t *testing.T) {
	dir := t.TempDir()
	initSafeStore(t, dir)
	injectFS(t, "fuse", fsFuse)

	s, err := New(dir, quietLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	_, execErr := s.WriteDB().ExecContext(context.Background(),
		"INSERT INTO meta(key, value) VALUES('x','1')")
	require.Error(t, execErr, "a direct write on the read-only store's connection is rejected by SQLite")
}

// TestStoreNew_LocalFS_ReadWrite_Unchanged: no regression on a safe FS.
func TestStoreNew_LocalFS_ReadWrite_Unchanged(t *testing.T) {
	injectFS(t, "apfs", fsLocal)
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(context.Background(),
			"INSERT INTO meta(key, value) VALUES('t','1') ON CONFLICT(key) DO UPDATE SET value='1'")
		return e
	}), "writes work normally on a local filesystem")
}
