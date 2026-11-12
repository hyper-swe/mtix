// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

var errFakeReset = errors.New("fake reset")

// fakeRows is a driver.Rows that records closes and answers every optional
// column-metadata interface with a fixed value.
type fakeRows struct {
	closed int
	steps  int
}

func (r *fakeRows) Columns() []string { return []string{"a"} }
func (r *fakeRows) Close() error      { r.closed++; return nil }
func (r *fakeRows) Next([]driver.Value) error {
	r.steps++
	return io.EOF
}
func (*fakeRows) ColumnTypeDatabaseTypeName(int) string             { return "FAKETYPE" }
func (*fakeRows) ColumnTypeLength(int) (int64, bool)                { return 7, true }
func (*fakeRows) ColumnTypeNullable(int) (bool, bool)               { return true, true }
func (*fakeRows) ColumnTypePrecisionScale(int) (int64, int64, bool) { return 5, 3, true }
func (*fakeRows) ColumnTypeScanType(int) reflect.Type               { return reflect.TypeOf(int64(0)) }

// bareRows implements only driver.Rows, so the optional metadata is absent.
type bareRows struct{}

func (bareRows) Columns() []string         { return []string{"a"} }
func (bareRows) Close() error              { return nil }
func (bareRows) Next([]driver.Value) error { return io.EOF }

// fakeStmt records the context its queries and execs receive and mimics the
// driver bug: a query whose context is cancelled drops its rows unclosed.
type fakeStmt struct {
	rows     *fakeRows
	queryErr error // the context error the query saw, if any
	execs    int
	closed   int
	cancel   func() // cancels the caller's context while the query runs
}

func (s *fakeStmt) Close() error                               { s.closed++; return nil }
func (s *fakeStmt) NumInput() int                              { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(0), nil }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error)  { return s.rows, nil }
func (s *fakeStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	s.execs++
	return driver.RowsAffected(1), nil
}
func (s *fakeStmt) QueryContext(ctx context.Context, _ []driver.NamedValue) (driver.Rows, error) {
	if s.cancel != nil {
		s.cancel()
	}
	if err := ctx.Err(); err != nil {
		s.queryErr = err
		return nil, err // the driver bug: rows dropped without Close
	}
	return s.rows, nil
}

// fakeConn is a driver.Conn with every optional interface the shield forwards.
type fakeConn struct {
	stmt     *fakeStmt
	resets   int
	valid    bool
	pings    int
	prepared int
	cancel   func()
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { c.prepared++; return c.stmt, nil }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (c *fakeConn) ResetSession(context.Context) error  { c.resets++; return errFakeReset }
func (c *fakeConn) IsValid() bool                       { return c.valid }
func (c *fakeConn) Ping(context.Context) error          { c.pings++; return errFakeReset }
func (c *fakeConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	if c.cancel != nil {
		c.cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.stmt.rows, nil
}

// TestShieldConn_OptionalInterfaces_ForwardToDriver proves the shield passes
// ResetSession, IsValid and Ping through to the driver connection, because
// database/sql uses ResetSession to discard an interrupted connection
// (MTIX-250).
func TestShieldConn_OptionalInterfaces_ForwardToDriver(t *testing.T) {
	inner := &fakeConn{stmt: &fakeStmt{rows: &fakeRows{}}, valid: false}
	c := &shieldConn{Conn: inner}

	require.ErrorIs(t, c.ResetSession(t.Context()), errFakeReset)
	require.Equal(t, 1, inner.resets, "ResetSession reached the driver connection")
	require.False(t, c.IsValid(), "IsValid reports the driver connection's answer")
	require.ErrorIs(t, c.Ping(t.Context()), errFakeReset)
	require.Equal(t, 1, inner.pings)

	bare := &shieldConn{Conn: bareConn{}}
	require.NoError(t, bare.ResetSession(t.Context()))
	require.True(t, bare.IsValid(), "a driver without Validator is valid")
	require.NoError(t, bare.Ping(t.Context()))
}

// bareConn implements only driver.Conn.
type bareConn struct{}

func (bareConn) Prepare(string) (driver.Stmt, error) { return &fakeStmt{rows: &fakeRows{}}, nil }
func (bareConn) Close() error                        { return nil }
func (bareConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }

// TestShieldRows_ColumnMetadata_ForwardsDriverAnswers proves every optional
// column-type interface of the driver rows reaches database/sql through the
// shield, and that rows without them get the neutral defaults (MTIX-250).
func TestShieldRows_ColumnMetadata_ForwardsDriverAnswers(t *testing.T) {
	r := &shieldRows{Rows: &fakeRows{}, ctx: t.Context()}
	require.Equal(t, "FAKETYPE", r.ColumnTypeDatabaseTypeName(0))
	n, ok := r.ColumnTypeLength(0)
	require.True(t, ok)
	require.EqualValues(t, 7, n)
	null, ok := r.ColumnTypeNullable(0)
	require.True(t, null && ok)
	p, s, ok := r.ColumnTypePrecisionScale(0)
	require.True(t, ok)
	require.EqualValues(t, 5, p)
	require.EqualValues(t, 3, s)
	require.Equal(t, reflect.TypeOf(int64(0)), r.ColumnTypeScanType(0))

	b := &shieldRows{Rows: bareRows{}, ctx: t.Context()}
	require.Equal(t, "", b.ColumnTypeDatabaseTypeName(0))
	n, ok = b.ColumnTypeLength(0)
	require.False(t, ok)
	require.EqualValues(t, 0, n)
	_, ok = b.ColumnTypeNullable(0)
	require.False(t, ok)
	_, _, ok = b.ColumnTypePrecisionScale(0)
	require.False(t, ok)
	require.Equal(t, reflect.TypeOf(new(any)).Elem(), b.ColumnTypeScanType(0))
}

// TestShield_QueryCancelledWhileRunning_DriverNeverSeesTheCancel reproduces
// the driver bug deterministically with a fake driver that drops its rows
// when the context it was given is cancelled: the caller's context is
// cancelled inside the driver call, and neither a connection query nor a
// prepared statement's query may deliver that cancel to the driver (MTIX-250).
func TestShield_QueryCancelledWhileRunning_DriverNeverSeesTheCancel(t *testing.T) {
	t.Run("connection query", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		inner := &fakeConn{stmt: &fakeStmt{rows: &fakeRows{}}, cancel: cancel}
		rows, err := (&shieldConn{Conn: inner}).QueryContext(ctx, "q", nil)
		require.NoError(t, err, "the driver ran on a context the cancel did not reach")
		require.ErrorIs(t, rows.Next(make([]driver.Value, 1)), context.Canceled, "the shield enforces the cancel between rows")
		require.Equal(t, 1, inner.stmt.rows.closed, "the driver rows were closed exactly once")
		require.NoError(t, rows.Close())
		require.Equal(t, 1, inner.stmt.rows.closed)
	})
	t.Run("prepared statement query", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		inner := &fakeConn{stmt: &fakeStmt{rows: &fakeRows{}}}
		inner.stmt.cancel = cancel
		st, err := (&shieldConn{Conn: inner}).PrepareContext(ctx, "q")
		require.NoError(t, err)
		require.IsType(t, &shieldStmt{}, st, "a prepared statement is wrapped")
		rows, err := st.(driver.StmtQueryContext).QueryContext(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, inner.stmt.queryErr, "the driver statement never saw the cancel")
		require.ErrorIs(t, rows.Next(make([]driver.Value, 1)), context.Canceled)
		require.Equal(t, 1, inner.stmt.rows.closed)
	})
	t.Run("pre-cancelled statement query is refused", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		st := &shieldStmt{Stmt: &fakeStmt{rows: &fakeRows{}}}
		_, err := st.QueryContext(ctx, nil)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "read cancelled before start")
	})
}

// TestShieldStmt_ExecAndPlainQuery_ForwardToDriver proves the statement
// wrapper keeps the driver statement's exec and context-free query, and that
// prepare falls back to Prepare for a driver without PrepareContext (MTIX-250).
func TestShieldStmt_ExecAndPlainQuery_ForwardToDriver(t *testing.T) {
	inner := &fakeConn{stmt: &fakeStmt{rows: &fakeRows{}}}
	st, err := (&shieldConn{Conn: inner}).PrepareContext(t.Context(), "q")
	require.NoError(t, err)
	require.Equal(t, 1, inner.prepared, "the driver's Prepare was used")
	res, err := st.(driver.StmtExecContext).ExecContext(t.Context(), nil)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.Equal(t, 1, inner.stmt.execs)
	rows, err := st.Query(nil) //nolint:staticcheck // SA1019: the context-free driver Query is exactly what this asserts is forwarded
	require.NoError(t, err)
	require.Same(t, inner.stmt.rows, rows, "the context-free query is the driver's")
	require.NoError(t, st.Close())
	require.Equal(t, 1, inner.stmt.closed)

	plain := &shieldStmt{Stmt: bareStmt{}}
	_, err = plain.QueryContext(t.Context(), nil)
	require.ErrorIs(t, err, driver.ErrSkip)
	_, err = plain.ExecContext(t.Context(), nil)
	require.ErrorIs(t, err, driver.ErrSkip)
}

// bareStmt implements only driver.Stmt.
type bareStmt struct{}

func (bareStmt) Close() error                               { return nil }
func (bareStmt) NumInput() int                              { return -1 }
func (bareStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(0), nil }
func (bareStmt) Query([]driver.Value) (driver.Rows, error)  { return &fakeRows{}, nil }

// TestOpenShieldedFrom_DriverProbe_IsClosed proves the throwaway handle used to
// reach the driver is closed, so the shielded open leaves no extra pool behind
// (MTIX-250).
func TestOpenShieldedFrom_DriverProbe_IsClosed(t *testing.T) {
	var probe *sql.DB
	open := func(path string, params ...string) (*sql.DB, error) {
		db, err := openSQLite(path, params...)
		probe = db
		return db, err
	}
	pool, err := openShieldedFrom(open, filepath.Join(t.TempDir(), "probe.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	require.NotNil(t, probe)
	require.Error(t, probe.PingContext(t.Context()), "the probe handle is closed")
	require.NoError(t, pool.PingContext(t.Context()), "the shielded pool itself is open")
}
