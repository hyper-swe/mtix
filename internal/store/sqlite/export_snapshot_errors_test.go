// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var errSnapshotRead = errors.New("owned snapshot read fault")
var errSnapshotRollback = errors.New("owned snapshot rollback fault")

type snapshotFault struct {
	stage                      string
	failed                     atomic.Bool
	begins, commits, rollbacks atomic.Int64
	readOnly                   atomic.Bool
	cancel                     context.CancelFunc
}

func (f *snapshotFault) consume(stage string) bool {
	return f.stage == stage && f.failed.CompareAndSwap(false, true)
}

type snapshotFaultConn struct {
	snapshotNativeConn
	fault *snapshotFault
}

func (c *snapshotFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.fault.begins.Add(1)
	c.fault.readOnly.Store(opts.ReadOnly)
	if c.fault.consume("begin") {
		return nil, errSnapshotRead
	}
	tx, err := c.snapshotNativeConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &snapshotFaultTx{Tx: tx, fault: c.fault}, nil
}
func (c *snapshotFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	for _, table := range []string{"nodes", "dependencies", "agents", "sessions"} {
		if strings.Contains(query, "FROM "+table) && c.fault.consume("query-"+table) {
			return nil, errSnapshotRead
		}
	}
	if c.fault.consume("rollback") {
		return nil, errSnapshotRead
	}
	if c.fault.consume("cancel") {
		c.fault.cancel()
	}
	return c.snapshotNativeConn.QueryContext(ctx, query, args)
}

type snapshotFaultTx struct {
	driver.Tx
	fault *snapshotFault
}

func (tx *snapshotFaultTx) Commit() error {
	tx.fault.commits.Add(1)
	// A report fault AFTER native commit tests error propagation without faking
	// native SQLite's cleanup or stranding an invented unfinished transaction.
	err := tx.Tx.Commit()
	if tx.fault.consume("commit-report") {
		return errors.Join(err, errSnapshotRead)
	}
	return err
}
func (tx *snapshotFaultTx) Rollback() error {
	tx.fault.rollbacks.Add(1)
	err := tx.Tx.Rollback()
	if tx.fault.stage == "rollback" && tx.fault.failed.Load() {
		return errors.Join(err, errSnapshotRollback)
	}
	return err
}
func TestExportSnapshot_ErrorCleanupAndReuse(t *testing.T) {
	stages := []string{"begin", "query-nodes", "query-dependencies", "query-agents", "query-sessions", "rows-close", "scan", "cancel", "rollback", "commit-report"}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) { snapshotErrorCleanup(t, stage) })
	}
}
func snapshotErrorCleanup(t *testing.T, stage string) {
	store := snapshotProcessStore(t, t.TempDir())
	snapshotSeed(t, store)
	fault := &snapshotFault{stage: stage}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fault.cancel = cancel
	connector := &snapshotConnector{path: store.dbPath, wrap: func(c snapshotNativeConn) snapshotNativeConn {
		return &snapshotFaultConn{snapshotNativeConn: c, fault: fault}
	}, afterClose: func() error {
		if fault.consume("rows-close") {
			return errSnapshotRead
		}
		return nil
	}}
	wrapped := sql.OpenDB(connector)
	wrapped.SetMaxOpenConns(1)
	original := store.readDB
	store.readDB = wrapped
	t.Cleanup(func() { store.readDB = original; require.NoError(t, wrapped.Close()) })
	if stage == "scan" {
		_, err := store.WriteDB().Exec(`UPDATE nodes SET annotations='{unreadable'`)
		require.NoError(t, err)
	}
	data, err := store.Export(ctx, "SNAP", "test")
	require.Error(t, err)
	require.Nil(t, data, "failed export must never leak partial data")
	require.True(t, fault.readOnly.Load(), "export transaction must use reader semantics")
	snapshotAssertError(t, stage, err)
	if stage == "scan" {
		_, err := store.WriteDB().Exec(`UPDATE nodes SET annotations='[]'`)
		require.NoError(t, err)
	}
	// A real new transaction in this pool limited to one connection must succeed, proving
	// rollback/rows cleanup; timeout is solely a leaked-resource hang guard.
	reuseCtx, reuseCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer reuseCancel()
	reused, err := store.Export(reuseCtx, "SNAP", "test")
	require.NoError(t, err)
	require.NoError(t, ValidateExport(reused))
	require.Equal(t, 1, reused.NodeCount)
	require.Equal(t, int64(2), fault.begins.Load())
	require.Zero(t, wrapped.Stats().InUse)
	if stage != "begin" && stage != "commit-report" {
		require.Equal(t, int64(1), fault.rollbacks.Load(), "native failed read transaction must rollback exactly once")
	}
}
func snapshotAssertError(t *testing.T, stage string, err error) {
	t.Helper()
	switch stage {
	case "cancel":
		require.ErrorIs(t, err, context.Canceled)
	case "scan":
		require.ErrorContains(t, err, "scan export node")
	default:
		require.ErrorIs(t, err, errSnapshotRead)
	}
	if stage == "rollback" {
		require.ErrorIs(t, err, errSnapshotRollback, "both read and rollback errors must remain visible")
	}
}
