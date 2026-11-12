// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadPool_ContextCancelledBeforeRead_IsRefused proves a read whose
// context is already cancelled is refused before any statement runs: through
// the pool methods, and through the shielded connection itself (MTIX-250).
func TestReadPool_ContextCancelledBeforeRead_IsRefused(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	t.Run("QueryContext", func(t *testing.T) {
		rows, err := st.readDB.QueryContext(ctx, `SELECT 1`)
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, rows)
	})
	t.Run("QueryRowContext", func(t *testing.T) {
		var n int
		require.ErrorIs(t, st.readDB.QueryRowContext(ctx, `SELECT 1`).Scan(&n), context.Canceled)
	})
	t.Run("BeginTx", func(t *testing.T) {
		tx, err := st.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if tx != nil {
			_ = tx.Rollback()
		}
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("shielded connection", func(t *testing.T) {
		conn := openShieldedConn(t)
		rows, err := conn.(driver.QueryerContext).QueryContext(ctx, `SELECT 1`, nil)
		if rows != nil {
			_ = rows.Close()
		}
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "read cancelled before start")
	})
	t.Run("detachedRead", func(t *testing.T) {
		dctx, release, err := detachedRead(ctx)
		defer release()
		require.Nil(t, dctx)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// openShieldedConn opens one driver connection of a fresh shielded pool.
func openShieldedConn(t *testing.T) driver.Conn {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shield.db")
	db, err := openShieldedSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	c, err := shieldConnector{drv: db.Driver(), dsn: sqliteDSN(path)}.Connect(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestReadPool_CancelledBetweenRows_StopsAndFreesConnection proves the shield
// keeps the caller's cancellation: a context cancelled while rows are being
// read ends the iteration with the context's error, and the next read on the
// same one-connection pool sees a fresh write (MTIX-250).
func TestReadPool_CancelledBetweenRows_StopsAndFreesConnection(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	seedCancelFixture(t, st)
	st.readDB.SetMaxOpenConns(1)

	ctx, cancel := context.WithCancel(t.Context())
	rows, err := st.readDB.QueryContext(ctx, `SELECT id FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.True(t, rows.Next(), "the first row is read before the cancel")
	cancel()
	read := 1
	for rows.Next() {
		read++
	}
	require.ErrorIs(t, rows.Err(), context.Canceled)
	require.Less(t, read, 33, "iteration stops instead of reading every row")

	_, err = st.writeDB.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES('between-rows','v')`)
	require.NoError(t, err)
	var n int
	require.NoError(t, st.readDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM meta WHERE key='between-rows'`).Scan(&n))
	require.Equal(t, 1, n, "the connection the cancelled read used shows the fresh write")
}

// TestReadPool_CompletedReads_ReturnEveryRow proves the shield changes nothing
// for a read that is not cancelled: all rows, column types and an empty result
// come back as the driver gives them (MTIX-250).
func TestReadPool_CompletedReads_ReturnEveryRow(t *testing.T) {
	st, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	seedCancelFixture(t, st)

	rows, err := st.readDB.QueryContext(t.Context(), `SELECT id, seq FROM nodes WHERE project = ? ORDER BY seq`, "MTIX")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	types, err := rows.ColumnTypes()
	require.NoError(t, err)
	require.Len(t, types, 2)
	count := 0
	for rows.Next() {
		count++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 33, count, "30 roots and 3 children")

	var none int
	err = st.readDB.QueryRowContext(t.Context(), `SELECT 1 FROM nodes WHERE id = ?`, "absent").Scan(&none)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.Equal(t, fmt.Sprint(0), fmt.Sprint(none))
}
