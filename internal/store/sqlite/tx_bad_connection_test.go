// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sqliteDriver "modernc.org/sqlite"
)

// commitFaultConnector injects a COMMIT error on the first real SQLite
// connection. Later connections perform ordinary transactions.
type commitFaultConnector struct {
	name      string
	commitErr error
	opened    atomic.Int32
}

func (c *commitFaultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.name)
	if err != nil {
		return nil, err
	}
	var fault error
	if c.opened.Add(1) == 1 {
		fault = c.commitErr
	}
	return &commitFaultConn{Conn: conn, commitErr: fault}, nil
}

func (*commitFaultConnector) Driver() driver.Driver { return &sqliteDriver.Driver{} }

type commitFaultConn struct {
	driver.Conn
	commitErr error
}

func (c *commitFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("SQLite driver does not implement ConnBeginTx")
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, commitErr: c.commitErr}, nil
}

type commitFaultTx struct {
	driver.Tx
	commitErr error
}

func (tx *commitFaultTx) Commit() error {
	if tx.commitErr != nil {
		return tx.commitErr
	}
	return tx.Tx.Commit()
}

type badCommitCase struct {
	name       string
	commitErr  error
	rollback   error
	onlyCommit bool
}

// TestWriterTx_BadCommit_ReportsOnlyMeaningfulErrors exercises database/sql's
// real discard after ErrBadConn, including wrapped causes and genuine recovery
// failures that must not be hidden beside a closed-connection error.
func TestWriterTx_BadCommit_ReportsOnlyMeaningfulErrors(t *testing.T) {
	other := errors.New("rollback storage failure")
	tests := []badCommitCase{
		{"bad connection", driver.ErrBadConn, nil, true},
		{"wrapped bad connection", fmt.Errorf("driver commit: %w", driver.ErrBadConn), nil, true},
		{"bad connection rollback", driver.ErrBadConn, driver.ErrBadConn, true},
		{"wrapped closed rollback", driver.ErrBadConn, fmt.Errorf("rollback: %w", sql.ErrConnDone), true},
		{"real rollback failure", driver.ErrBadConn, other, false},
		{"mixed rollback failure", driver.ErrBadConn, errors.Join(other, sql.ErrConnDone), false},
		{"ordinary commit closed rollback", errors.New("commit storage failure"), sql.ErrConnDone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { checkBadCommit(t, tt) })
	}
}

func checkBadCommit(t *testing.T, tt badCommitCase) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connector := &commitFaultConnector{name: filepath.Join(t.TempDir(), "writer.db"), commitErr: tt.commitErr}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err := db.ExecContext(ctx, `CREATE TABLE probe (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	var rollback RollbackWriterFunc
	if tt.rollback != nil {
		rollback = func(context.Context, *sql.Conn) error { return tt.rollback }
	}
	wtx, err := BeginWriterTx(ctx, db, logger, rollback)
	require.NoError(t, err)
	defer wtx.Release(ctx)
	_, err = wtx.ExecContext(ctx, `INSERT INTO probe (id) VALUES (1)`)
	require.NoError(t, err)
	commitErr := wtx.Commit()
	require.ErrorIs(t, commitErr, tt.commitErr)
	wrapped := fmt.Errorf("commit transaction: %w", commitErr)
	got := wtx.RecoverAfterCommit(ctx, wrapped)
	require.ErrorIs(t, got, tt.commitErr)
	if tt.onlyCommit {
		require.EqualError(t, got, wrapped.Error(), "closed-connection noise must not be appended")
	} else {
		require.ErrorIs(t, got, tt.rollback, "a genuine recovery error must remain wrapped")
		require.ErrorContains(t, got, "writer recovery failed")
	}
	wtx.Release(ctx)
	require.Empty(t, logs.String(), "release must not log redundant closed-connection errors")
	require.Zero(t, db.Stats().InUse)
	requireReplacementWrite(ctx, t, db, logger)
	require.GreaterOrEqual(t, connector.opened.Load(), int32(2))
}

func requireReplacementWrite(ctx context.Context, t *testing.T, db *sql.DB, logger *slog.Logger) {
	t.Helper()
	wtx, err := BeginWriterTx(ctx, db, logger, nil)
	require.NoError(t, err)
	defer wtx.Release(ctx)
	_, err = wtx.ExecContext(ctx, `INSERT INTO probe (id) VALUES (2)`)
	require.NoError(t, err)
	require.NoError(t, wtx.Commit())
	wtx.Release(ctx)
	var id int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id FROM probe`).Scan(&id))
	require.Equal(t, 2, id, "the failed transaction must be absent and the replacement write committed")
}
