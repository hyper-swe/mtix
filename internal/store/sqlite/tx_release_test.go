// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	driver "modernc.org/sqlite"
)

// requireNextWriteWithin fails when the next write does not finish promptly,
// which is how a leaked or locked writer connection shows (MTIX-198).
func requireNextWriteWithin(t *testing.T, s *Store, d time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- s.WithTx(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(context.Background(), `INSERT OR REPLACE INTO meta (key, value) VALUES ('release.probe', 'x')`)
			return err
		})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(d):
		t.Fatal("the next write hung: the writer connection was not released")
	}
	require.Zero(t, s.writeDB.Stats().InUse, "no connection may stay in use")
}

// TestWriterTx_PanicInsideTransaction_ReleasesWriter panics between begin and
// commit. The widen and claim helpers have no deferred rollback of their own,
// so Release must end the live transaction or the pool hangs (MTIX-198).
func TestWriterTx_PanicInsideTransaction_ReleasesWriter(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		run  func(s *Store)
	}{
		{"WithTx", func(s *Store) {
			_ = s.WithTx(ctx, func(*sql.Tx) error { panic("boom") }) //nolint:errcheck // panics before returning
		}},
		{"withSchemaTx", func(s *Store) {
			_ = s.withSchemaTx(ctx, func(*sql.Tx) error { panic("boom") }) //nolint:errcheck // panics before returning
		}},
		{"WriterTx without a deferred rollback", func(s *Store) {
			wtx, err := s.beginWriterTx(ctx)
			require.NoError(t, err)
			defer wtx.Release(ctx)
			_, err = wtx.ExecContext(ctx, `INSERT OR REPLACE INTO meta (key, value) VALUES ('release.panic', 'x')`)
			require.NoError(t, err)
			panic("boom")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := bufferedStore(t)
			require.PanicsWithValue(t, "boom", func() { tt.run(s) })
			requireNextWriteWithin(t, s, 5*time.Second)
			var n int
			require.NoError(t, s.writeDB.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM meta WHERE key = 'release.panic'`).Scan(&n))
			require.Zero(t, n, "the panicked transaction must not persist")
		})
	}
}

// TestWriterTx_BeginFails_ReleasesPinnedConnection makes BEGIN IMMEDIATE fail
// with SQLITE_BUSY (another connection holds the write lock) and checks the
// pinned connection is returned, not leaked (MTIX-198).
func TestWriterTx_BeginFails_ReleasesPinnedConnection(t *testing.T) {
	ctx := context.Background()
	s, raw, _ := bufferedStore(t)
	_, err := s.writeDB.ExecContext(ctx, `PRAGMA busy_timeout = 1`)
	require.NoError(t, err)
	holder, err := raw.Conn(ctx)
	require.NoError(t, err)
	_, err = holder.ExecContext(ctx, `BEGIN IMMEDIATE`)
	require.NoError(t, err)

	err = s.WithTx(ctx, func(*sql.Tx) error { return nil })
	require.ErrorContains(t, err, "begin transaction")
	var se *driver.Error
	require.True(t, errors.As(err, &se), "the begin error must stay in the wrapped chain")
	require.Zero(t, s.writeDB.Stats().InUse, "a failed begin must not leak the pinned connection")

	_, err = holder.ExecContext(ctx, `ROLLBACK`)
	require.NoError(t, err)
	require.NoError(t, holder.Close())
	requireNextWriteWithin(t, s, 5*time.Second)
}

// TestCommitFailure_RecoveryFails_IsReportedByEveryHelper makes the recovery
// ROLLBACK fail after each helper's commit failure. The helper must REPORT
// both errors, wrapped, and not merely clean up (MTIX-198).
func TestCommitFailure_RecoveryFails_IsReportedByEveryHelper(t *testing.T) {
	for _, tt := range commitFaultCases() {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := bufferedStore(t)
			tt.arm(t, s)
			injected := errors.New("injected rollback failure")
			s.rollbackWriter = func(context.Context, *sql.Conn) error { return injected }
			err := tt.run(s)
			require.ErrorIs(t, err, injected, "the recovery error must be reported with %w")
			var se *driver.Error
			require.True(t, errors.As(err, &se), "the commit error must stay in the chain")
			require.Equal(t, 787, se.Code())
			s.rollbackWriter = nil
			requireWriterRecovered(t, s)
		})
	}
}
