// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Read-only handles returned to callers must shield every connection, even
// when a caller pins one or prepares a statement (MTIX-261.1).
func TestOpenStoreReadOnly_Connections_UseShield(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	db, err := openStoreReadOnly(st.dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.Raw(func(raw any) error {
		if _, ok := raw.(*shieldConn); !ok {
			return fmt.Errorf("read-only connection is unshielded: %T", raw)
		}
		return nil
	}))
}

// The timing sweep exercises both row and rows APIs, then checks visibility
// and checkpoint progress. The guard binds recovery's handles to this same
// shield; their existing recovery tests exercise the actual salvage queries.
func TestOpenStoreReadOnly_CancelledQuery_LeavesNoSnapshot(t *testing.T) {
	for _, route := range []string{"row", "rows"} {
		t.Run(route, func(t *testing.T) { sweepReadOnlyCancellation(t, route) })
	}
}

func sweepReadOnlyCancellation(t *testing.T, route string) {
	t.Helper()
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	seedWriterCancelFixture(t, st)
	db, err := openStoreReadOnly(st.dbPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	db.SetMaxOpenConns(1)
	cancelled := 0
	for i := 0; i < 1000; i++ {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { defer close(done); time.Sleep(time.Duration(i%40) * 5 * time.Microsecond); cancel() }()
		if route == "row" {
			var key string
			err = db.QueryRowContext(ctx, `SELECT key FROM meta ORDER BY key`).Scan(&key)
		} else {
			err = drainWriterRows(db.QueryContext(ctx, `SELECT key FROM meta ORDER BY key`))
		}
		cancel()
		<-done
		if err != nil {
			require.ErrorIs(t, err, context.Canceled)
			cancelled++
		}
		assertReadOnlyFresh(t, st, db, i)
	}
	require.Positive(t, cancelled)
}

func assertReadOnlyFresh(t *testing.T, st *Store, db *sql.DB, i int) {
	t.Helper()
	key := fmt.Sprintf("readonly-cancel-%d", i)
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES(?,?)`, key, "v")
		if err != nil {
			return fmt.Errorf("insert read-only probe: %w", err)
		}
		return nil
	}))
	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM meta WHERE key=?`, key).Scan(&n))
	require.Equal(t, 1, n, "stale snapshot at iteration %d", i)
	var busy, log, checkpointed int
	require.NoError(t, st.writeDB.QueryRowContext(t.Context(), `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &log, &checkpointed))
	require.Equal(t, log, checkpointed, "held read transaction at iteration %d", i)
}
