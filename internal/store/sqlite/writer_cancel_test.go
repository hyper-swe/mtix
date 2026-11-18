// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Writer cancellation tests exercise direct and transactional reads on the
// single writer, including prepared statements. The timing sweep targets the
// modernc first-step cancellation window; it is statistical, like MTIX-250.
func TestWriterReads_CancelledMidQuery_LeaveNoStaleSnapshot(t *testing.T) {
	for _, route := range []string{"query", "row", "prepared", "transaction"} {
		t.Run(route, func(t *testing.T) { sweepWriterCancellation(t, route) })
	}
}

func sweepWriterCancellation(t *testing.T, route string) {
	t.Helper()
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	peer, err := New(st.dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, peer.Close()) })
	seedWriterCancelFixture(t, st)
	cancelled := 0
	for i := 0; i < readCancelRouteIterations; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			time.Sleep(time.Duration(i%40) * 5 * time.Microsecond)
			cancel()
		}()
		err = cancelledWriterRead(ctx, t, st, route)
		cancel()
		<-done
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
			cancelled++
		}
		key := fmt.Sprintf("writer-cancel-%d", i)
		require.NoError(t, peer.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, e := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?)`, key, "v")
			if e != nil {
				return fmt.Errorf("insert probe: %w", e)
			}
			return nil
		}))
		var n int
		require.NoError(t, st.writeDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM meta WHERE key=?`, key).Scan(&n))
		require.Equal(t, 1, n, "stale writer snapshot after %s, iteration %d", route, i)
		var busy, log, checkpointed int
		require.NoError(t, peer.writeDB.QueryRowContext(t.Context(), `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &log, &checkpointed))
		require.Equal(t, log, checkpointed, "writer holds a read transaction after %s, iteration %d", route, i)
	}
	require.Positive(t, cancelled, "sweep must observe cancellation")
}

func cancelledWriterRead(ctx context.Context, t *testing.T, st *Store, route string) error {
	t.Helper()
	const query = `SELECT key FROM meta ORDER BY key`
	switch route {
	case "row":
		var id string
		return st.writeDB.QueryRowContext(ctx, query).Scan(&id)
	case "prepared":
		stmt, err := st.writeDB.PrepareContext(t.Context(), query)
		require.NoError(t, err)
		defer func() { require.NoError(t, stmt.Close()) }()
		return drainWriterRows(stmt.QueryContext(ctx))
	case "transaction":
		return st.WithTx(t.Context(), func(tx *sql.Tx) error {
			if err := drainWriterRows(tx.QueryContext(ctx, query)); err != nil {
				return fmt.Errorf("read in transaction: %w", err)
			}
			return nil
		})
	default:
		return drainWriterRows(st.writeDB.QueryContext(ctx, query))
	}
}

func drainWriterRows(rows *sql.Rows, err error) error {
	if err != nil {
		return fmt.Errorf("query writer: %w", err)
	}
	for rows.Next() {
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("drain writer: %w", err)
	}
	return nil
}

// seedWriterCancelFixture avoids unrelated sync identity and host interfaces.
func seedWriterCancelFixture(t *testing.T, st *Store) {
	t.Helper()
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		for i := 0; i < 30; i++ {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?)`, fmt.Sprintf("fixture-%d", i), "v"); err != nil {
				return fmt.Errorf("seed writer cancellation: %w", err)
			}
		}
		return nil
	}))
}

// Both the normal writer and the unsafe-filesystem read-only writer handle
// must shield every connection they hand to database/sql.
func TestWriterConnection_AllModes_UseShield(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("readOnly=%t", readOnly), func(t *testing.T) {
			db, err := openDB(t.Context(), st.dbPath, true, readOnly)
			require.NoError(t, err)
			defer func() { require.NoError(t, db.Close()) }()
			conn, err := db.Conn(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, conn.Close()) }()
			require.NoError(t, conn.Raw(func(raw any) error {
				if _, ok := raw.(*shieldConn); !ok {
					return fmt.Errorf("writer connection is unshielded: %T", raw)
				}
				return nil
			}))
		})
	}
}
