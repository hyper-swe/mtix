// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"

	native "modernc.org/sqlite"
)

// These are the optional interfaces implemented by the approved native driver.
// Embedding the composite forwards every operation except the wrapped query.
type snapshotNativeConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.QueryerContext
	driver.ExecerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}
type snapshotNativeRows interface {
	driver.Rows
	driver.RowsColumnTypeDatabaseTypeName
	driver.RowsColumnTypeLength
	driver.RowsColumnTypeNullable
	driver.RowsColumnTypePrecisionScale
	driver.RowsColumnTypeScanType
}
type snapshotConnector struct {
	path       string
	afterClose func() error
	once       sync.Once
	wrap       func(snapshotNativeConn) snapshotNativeConn
}

func (c *snapshotConnector) Driver() driver.Driver { return &native.Driver{} }
func (c *snapshotConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.Driver().Open(c.path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	forwarded, ok := conn.(snapshotNativeConn)
	if !ok {
		interfaceErr := errors.New("native fixture connection lacks delegated interfaces")
		if closeErr := conn.Close(); closeErr != nil {
			return nil, errors.Join(interfaceErr, fmt.Errorf("close unsupported native fixture connection: %w", closeErr))
		}
		return nil, interfaceErr
	}
	if c.wrap != nil {
		forwarded = c.wrap(forwarded)
	}
	return &snapshotConn{snapshotNativeConn: forwarded, connector: c}, nil
}

type snapshotConn struct {
	snapshotNativeConn
	connector *snapshotConnector
}

func (c *snapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.snapshotNativeConn.QueryContext(ctx, query, args)
	if err != nil || query != exportNodeSelectSQL+" ORDER BY id" {
		return rows, err
	}
	forwarded, ok := rows.(snapshotNativeRows)
	if !ok {
		interfaceErr := errors.New("native fixture rows lack delegated interfaces")
		if closeErr := rows.Close(); closeErr != nil {
			return nil, errors.Join(interfaceErr, fmt.Errorf("close unsupported native fixture rows: %w", closeErr))
		}
		return nil, interfaceErr
	}
	return &snapshotRows{snapshotNativeRows: forwarded, connector: c.connector}, nil
}

type snapshotRows struct {
	snapshotNativeRows
	connector *snapshotConnector
}

func (rows *snapshotRows) Close() error {
	if err := rows.snapshotNativeRows.Close(); err != nil {
		return err
	}
	var barrierErr error
	rows.connector.once.Do(func() { barrierErr = rows.connector.afterClose() })
	return barrierErr
}

var (
	_ driver.Connector   = (*snapshotConnector)(nil)
	_ snapshotNativeConn = (*snapshotConn)(nil)
	_ snapshotNativeRows = (*snapshotRows)(nil)
)
