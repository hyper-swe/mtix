// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCommitFailure_RollbackFails_DiscardsConnection injects a failing
// recovery ROLLBACK after a failed commit. The writer connection must be
// discarded and replaced (its temp table is gone), both errors reported with
// %w, and the replacement must carry the connection pragmas (SQL rule 2).
func TestCommitFailure_RollbackFails_DiscardsConnection(t *testing.T) {
	ctx := context.Background()
	s, _, _ := bufferedStore(t)
	armFault(t, s, "")
	_, err := s.writeDB.ExecContext(ctx, `CREATE TEMP TABLE conn_marker (x INTEGER)`)
	require.NoError(t, err)

	injected := errors.New("injected rollback failure")
	s.rollbackWriter = func(context.Context, *sql.Conn) error { return injected }

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `INSERT INTO fault_child(p) VALUES (999)`)
		return execErr
	})
	require.ErrorIs(t, err, injected, "the rollback error must be wrapped with %w")
	require.ErrorContains(t, err, "FOREIGN KEY", "the commit error must be reported too")

	s.rollbackWriter = nil
	requireWriterRecovered(t, s)

	_, err = s.writeDB.ExecContext(ctx, `SELECT x FROM conn_marker`)
	require.ErrorContains(t, err, "no such table", "the stuck connection must have been replaced")
	var fk, busy int
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk))
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy))
	var syncMode, autockpt int
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&syncMode))
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `PRAGMA wal_autocheckpoint`).Scan(&autockpt))
	require.Equal(t, 2, syncMode, "synchronous FULL on the replacement connection")
	require.Equal(t, 1000, autockpt, "wal_autocheckpoint on the replacement connection")
	require.Equal(t, 1, fk, "foreign_keys must be ON on the replacement connection")
	require.Equal(t, 5000, busy, "busy_timeout must be set on the replacement connection")
}

// TestCommitFailure_CancelledRecoveryContext_StillRecovers reaches the stuck
// writer (a real failed COMMIT) and recovers it with a caller context that is
// already cancelled: recovery must still ROLLBACK, keeping the connection
// rather than discarding it, and the next write must succeed.
func TestCommitFailure_CancelledRecoveryContext_StillRecovers(t *testing.T) {
	ctx := context.Background()
	s, _, _ := bufferedStore(t)
	armFault(t, s, "")
	_, err := s.writeDB.ExecContext(ctx, `CREATE TEMP TABLE conn_marker (x INTEGER)`)
	require.NoError(t, err)

	wtx, err := s.beginWriterTx(ctx)
	require.NoError(t, err)
	_, err = wtx.ExecContext(ctx, `INSERT INTO fault_child(p) VALUES (999)`)
	require.NoError(t, err)
	commitErr := wtx.Commit()
	require.ErrorContains(t, commitErr, "FOREIGN KEY")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Equal(t, commitErr, wtx.RecoverAfterCommit(cancelled, commitErr), "recovery must succeed and return the commit error")
	wtx.Release(cancelled)

	requireWriterRecovered(t, s)
	_, err = s.writeDB.ExecContext(ctx, `SELECT x FROM conn_marker`)
	require.NoError(t, err, "recovery under a cancelled caller must roll back, not discard")
}

// TestDefaultRollbackWriter_ClosedConnection_ReturnsError makes sure a ROLLBACK
// that fails for any reason other than no active transaction is an error.
func TestDefaultRollbackWriter_ClosedConnection_ReturnsError(t *testing.T) {
	ctx := context.Background()
	s, _, _ := bufferedStore(t)
	conn, err := s.writeDB.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, defaultRollbackWriter(ctx, conn), "no active transaction is clean")
	require.NoError(t, conn.Close())
	require.Error(t, defaultRollbackWriter(ctx, conn), "a closed connection is not clean")
}

// TestIsNoTransaction_Messages classifies the rollback errors.
func TestIsNoTransaction_Messages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no transaction", errors.New("SQL logic error: cannot rollback - no transaction is active (1)"), true},
		{"other", errors.New("disk I/O error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, isNoTransaction(tt.err)) })
	}
}

// TestWriterConnPragmas_NamesEveryPinnedPragma pins the DSN pragmas. The
// synchronous and autocheckpoint values equal the driver defaults, so a
// replacement connection cannot show their loss; the constant is asserted.
func TestWriterConnPragmas_NamesEveryPinnedPragma(t *testing.T) {
	for _, want := range []string{"foreign_keys(1)", "busy_timeout(5000)", "synchronous(2)", "wal_autocheckpoint(1000)"} {
		require.Contains(t, writerConnPragmas, "_pragma="+want)
	}
}
