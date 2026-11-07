// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

type alteredSalvageConn struct {
	snapshotNativeConn
	column   int
	hideScan bool
	changed  bool
}

func (c *alteredSalvageConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.snapshotNativeConn.QueryContext(ctx, query, args)
	if err != nil {
		return rows, err
	}
	if c.hideScan && query == salvageInboxScanSQL {
		return &alteredSalvageRows{Rows: rows, hide: true}, nil
	}
	if query == salvageInboxSQL && !c.changed {
		c.changed = true
		return &alteredSalvageRows{Rows: rows, column: c.column}, nil
	}
	return rows, nil
}

type alteredSalvageRows struct {
	driver.Rows
	column int
	hide   bool
}

func (r *alteredSalvageRows) Next(values []driver.Value) error {
	if r.hide {
		return io.EOF
	}
	err := r.Rows.Next(values)
	if err == nil {
		values[r.column] = "different"
	}
	return err
}

func alteredSalvageDB(t *testing.T, column int, hide bool) *sql.DB {
	t.Helper()
	s := exchangeSnapshotStore(t)
	for _, id := range []string{"first", "second"} {
		_, err := s.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,'TEST','TEST-1','comment','{"to":"worker","body":"mail"}',0,0,'{}','sender','','2026-11-05T00:00:00Z')`, id)
		require.NoError(t, err)
	}
	db := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn {
		return &alteredSalvageConn{snapshotNativeConn: c, column: column, hideScan: hide, changed: hide}
	}})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func TestInboxRowidWalk_PointIdentityMustMatchEveryField(t *testing.T) {
	for column, name := range []string{"event", "project", "node", "uid", "author", "payload", "created"} {
		t.Run(name, func(t *testing.T) {
			db := alteredSalvageDB(t, column, false)
			walk := inboxRowidWalk{next: 1}
			row, _, ok := walk.read(t.Context(), db)
			require.True(t, ok)
			require.Equal(t, "second", row.e.EventID)
			require.Equal(t, 1, walk.failed)
			require.ErrorContains(t, walk.resultError(), "disagrees with point read")
		})
	}
}

func TestInboxRowidWalk_PointOperationMustMatch(t *testing.T) {
	db := alteredSalvageDB(t, 8, false)
	walk := inboxRowidWalk{next: 1}
	row, op, ok := walk.read(t.Context(), db)
	require.True(t, ok)
	require.Equal(t, "second", row.e.EventID)
	require.Equal(t, "comment", op)
	require.ErrorContains(t, walk.resultError(), "disagrees with point read")
}

func TestCollectSalvageInboxRows_WalkOnlyRow_ReportsDisagreement(t *testing.T) {
	db := alteredSalvageDB(t, 0, true)
	rows, err := collectSalvageInboxRows(t.Context(), db)
	require.Len(t, rows, 2)
	require.ErrorContains(t, err, "sequential scan and verified rowid walk disagree")
}

func TestRecoverInboxJournal_RejectedExportWithHealthyDatabase_IsIncomplete(t *testing.T) {
	s := exchangeSnapshotStore(t)
	res := &RecoverResult{}
	invalid := &ExportData{InboxJournal: &InboxJournal{Version: 2}}
	sourceBefore, statErr := os.Stat(s.dbPath)
	require.NoError(t, statErr)
	journal := recoverInboxJournal(t.Context(), s.dbPath, nil, res, inboxIntegrityTimeout, sourceBefore, invalid)
	require.NotNil(t, journal)
	require.True(t, res.InboxIncomplete)
	require.Len(t, res.Notes, 2, "healthy database must not contribute an incidental incomplete flag")
	require.Contains(t, res.Notes[0], "export inbox journal rejected")
}
