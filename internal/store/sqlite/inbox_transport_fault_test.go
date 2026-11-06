// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type inboxFaultConn struct {
	snapshotNativeConn
	stage string
}

func (c *inboxFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query != exportInboxJournalSQL && query != salvageInboxSQL {
		return c.snapshotNativeConn.QueryContext(ctx, query, args)
	}
	if c.stage == "query" {
		return nil, errSnapshotRead
	}
	rows, err := c.snapshotNativeConn.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &inboxFaultRows{Rows: rows, stage: c.stage, point: query == salvageInboxSQL}, nil
}

type inboxFaultRows struct {
	driver.Rows
	stage string
	point bool
}

func (r *inboxFaultRows) Next(values []driver.Value) error {
	if r.point && r.stage == "iteration" {
		return errSnapshotRead
	}
	err := r.Rows.Next(values)
	if r.stage == "iteration" && errors.Is(err, io.EOF) {
		return errSnapshotRead
	}
	if r.stage == "scan" && err == nil {
		values[0] = nil
	}
	return err
}
func (r *inboxFaultRows) Close() error {
	err := r.Rows.Close()
	if r.stage == "close" {
		return errors.Join(err, errSnapshotRead)
	}
	return err
}

func TestExportWithInbox_ReadFault_NeverPublishesPartialExport(t *testing.T) {
	for _, stage := range []string{"query", "scan", "iteration", "close"} {
		t.Run(stage, func(t *testing.T) {
			s := exchangeSnapshotStore(t)
			_, err := s.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES('fault-mail','TEST','TEST-1','comment','{"to":"worker","body":"mail"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`)
			require.NoError(t, err)
			wrapped := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn {
				return &inboxFaultConn{snapshotNativeConn: c, stage: stage}
			}})
			wrapped.SetMaxOpenConns(1)
			original := s.readDB
			s.readDB = wrapped
			t.Cleanup(func() { s.readDB = original; require.NoError(t, wrapped.Close()) })
			data, err := s.ExportWithInbox(t.Context(), "TEST", "")
			require.Error(t, err)
			require.Nil(t, data)
			require.Zero(t, wrapped.Stats().InUse, "failed export releases its read transaction")
			report := &RecoverResult{}
			salvaged, err := salvageInboxRows(t.Context(), wrapped, report)
			if stage == "scan" {
				require.ErrorContains(t, err, "1 unreadable rowid positions probed")
				require.Empty(t, salvaged.Events)
			} else {
				require.ErrorIs(t, err, errSnapshotRead)
			}
			require.Zero(t, wrapped.Stats().InUse, "failed salvage releases its rows")
		})
	}
}
