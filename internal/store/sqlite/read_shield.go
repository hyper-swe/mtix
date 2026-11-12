// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
)

// The reader pool opens its connections through shieldConnector (MTIX-250).
//
// Why: in modernc.org/sqlite v1.35.0, stmt.query drops the already-stepped rows
// without closing them when the context is cancelled after the first step. The
// statement is then never finalized, so the pooled reader connection keeps an
// open WAL read snapshot and a later read on it misses newer commits. Closing
// or discarding that connection was rejected: the unfinalized statement keeps
// the connection's snapshot alive, the caller never receives the rows to close,
// and closing the connection only leaves a zombie handle that holds the read
// mark (sqlite3_close_v2) and starves checkpoints. So the shield runs every query on a context its caller's cancellation
// cannot reach (detachedRead), which lets the driver return its rows, and
// enforces the caller's cancellation itself between rows: a cancelled context
// closes the driver rows (finalizing the statement) and fails the next read.
//
// Limit: the driver can no longer interrupt a query once it has started, and
// the shield sets no time bound of its own. A cancelled read therefore stops
// between rows; a single long first step (PRAGMA integrity_check, a large
// aggregate) finishes before the cancel takes effect, because interrupting it
// is exactly what leaves the stale snapshot. A read stuck on a lock is still
// bounded by busy_timeout. Prepared statements (shieldStmt) get the same
// treatment as direct queries.

// shieldConnector opens driver connections wrapped in shieldConn.
type shieldConnector struct {
	drv driver.Driver
	dsn string
}

// Connect opens one driver connection and shields it.
func (c shieldConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, fmt.Errorf("open shielded connection: %w", err)
	}
	return &shieldConn{Conn: inner}, nil
}

// Driver returns the wrapped driver.
func (c shieldConnector) Driver() driver.Driver { return c.drv }

// shieldConn forwards everything to the driver connection except QueryContext.
// The embedded driver.Conn supplies Prepare, Close and Begin.
type shieldConn struct {
	driver.Conn
}

// QueryContext runs the query on a context the caller's cancellation cannot
// reach and returns rows that enforce that cancellation themselves.
func (c *shieldConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return shieldQuery(ctx, func(dctx context.Context) (driver.Rows, error) {
		return q.QueryContext(dctx, query, args) //nolint:wrapcheck // the driver's own error, passed through unchanged for database/sql
	})
}

// shieldQuery runs one driver query on a context the caller's cancellation
// cannot reach and returns rows that enforce that cancellation themselves. The
// connection's queries and its prepared statements' queries both go through it.
func shieldQuery(ctx context.Context, run func(dctx context.Context) (driver.Rows, error)) (driver.Rows, error) {
	dctx, release, err := detachedRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := run(dctx)
	if err != nil {
		return nil, err
	}
	return &shieldRows{Rows: rows, ctx: ctx}, nil
}

// ExecContext forwards to the driver.
func (c *shieldConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, query, args) //nolint:wrapcheck // forwarded unchanged for database/sql
}

// PrepareContext prepares on the driver and shields the statement, so a read
// through a prepared statement is protected like a direct query.
func (c *shieldConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		st  driver.Stmt
		err error
	)
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		st, err = p.PrepareContext(ctx, query)
	} else {
		st, err = c.Prepare(query)
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // forwarded unchanged for database/sql
	}
	return &shieldStmt{Stmt: st}, nil
}

// shieldStmt wraps a driver statement so its queries run shielded. The
// embedded driver.Stmt supplies Close, NumInput, Exec and Query (the context
// free Query already runs on a background context).
type shieldStmt struct {
	driver.Stmt
}

// QueryContext runs the statement's query shielded, as shieldConn does.
func (s *shieldStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := s.Stmt.(driver.StmtQueryContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return shieldQuery(ctx, func(dctx context.Context) (driver.Rows, error) {
		return q.QueryContext(dctx, args) //nolint:wrapcheck // the driver's own error, passed through unchanged for database/sql
	})
}

// ExecContext forwards to the driver statement.
func (s *shieldStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	e, ok := s.Stmt.(driver.StmtExecContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return e.ExecContext(ctx, args) //nolint:wrapcheck // forwarded unchanged for database/sql
}

// BeginTx forwards to the driver.
func (c *shieldConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return c.Conn.Begin() //nolint:staticcheck // SA1019: the driver's fallback for a connection without BeginTx
	}
	return b.BeginTx(ctx, opts) //nolint:wrapcheck // forwarded unchanged for database/sql
}

// Ping forwards to the driver.
func (c *shieldConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx) //nolint:wrapcheck // forwarded unchanged for database/sql
	}
	return nil
}

// ResetSession forwards to the driver.
func (c *shieldConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx) //nolint:wrapcheck // forwarded unchanged for database/sql
	}
	return nil
}

// IsValid forwards to the driver.
func (c *shieldConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// shieldRows wraps driver rows so the caller's cancellation is enforced at row
// boundaries, where the driver rows can still be closed properly.
type shieldRows struct {
	driver.Rows
	ctx    context.Context //nolint:containedctx // the rows' caller context, checked on each Next
	closed bool            // database/sql serializes Next and Close, so no lock is needed
}

// Close closes the driver rows once; the driver finalizes its statement on every
// close, so a second close (the cancelled Next, then database/sql) must not
// reach it.
func (r *shieldRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.Rows.Close() //nolint:wrapcheck // forwarded unchanged for database/sql
}

// Next fails with the context's error once the caller's context is cancelled,
// after closing the driver rows so their statement is finalized.
func (r *shieldRows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	if err := r.ctx.Err(); err != nil {
		if cerr := r.Close(); cerr != nil {
			return fmt.Errorf("close cancelled rows: %w", cerr)
		}
		return err //nolint:wrapcheck // database/sql compares the context error as is
	}
	return r.Rows.Next(dest) //nolint:wrapcheck // io.EOF must reach database/sql unwrapped
}

// ColumnTypeDatabaseTypeName forwards the optional driver column metadata.
func (r *shieldRows) ColumnTypeDatabaseTypeName(index int) string {
	if v, ok := r.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return v.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

// ColumnTypeLength forwards the optional driver column metadata.
func (r *shieldRows) ColumnTypeLength(index int) (int64, bool) {
	if v, ok := r.Rows.(driver.RowsColumnTypeLength); ok {
		return v.ColumnTypeLength(index)
	}
	return 0, false
}

// ColumnTypeNullable forwards the optional driver column metadata.
func (r *shieldRows) ColumnTypeNullable(index int) (bool, bool) {
	if v, ok := r.Rows.(driver.RowsColumnTypeNullable); ok {
		return v.ColumnTypeNullable(index)
	}
	return false, false
}

// ColumnTypePrecisionScale forwards the optional driver column metadata.
func (r *shieldRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if v, ok := r.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return v.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

// ColumnTypeScanType forwards the optional driver column metadata.
func (r *shieldRows) ColumnTypeScanType(index int) reflect.Type {
	if v, ok := r.Rows.(driver.RowsColumnTypeScanType); ok {
		return v.ColumnTypeScanType(index)
	}
	return reflect.TypeOf(new(any)).Elem()
}
