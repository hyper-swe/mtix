// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/require"
)

type flippedSeekConn struct {
	snapshotNativeConn
	flipped bool
}

func (c *flippedSeekConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.snapshotNativeConn.QueryContext(ctx, query, args)
	if err != nil || query != salvageInboxSQL || c.flipped {
		return rows, err
	}
	c.flipped = true
	return &flippedSeekRows{Rows: rows}, nil
}

type flippedSeekRows struct{ driver.Rows }

func (r *flippedSeekRows) Next(values []driver.Value) error {
	err := r.Rows.Next(values)
	if err == nil {
		values[7] = int64(8363)
	}
	return err
}

func TestInboxRowidWalk_UnverifiableSeek_DoesNotSkipToReturnedPosition(t *testing.T) {
	s := exchangeSnapshotStore(t)
	for _, id := range []string{"first", "second"} {
		_, err := s.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,'TEST','TEST-1','comment','{"to":"worker","body":"mail"}',0,0,'{}','sender','','2026-11-05T00:00:00Z')`, id)
		require.NoError(t, err)
	}
	db := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn { return &flippedSeekConn{snapshotNativeConn: c} }})
	db.SetMaxOpenConns(1)
	defer func() { require.NoError(t, db.Close()) }()
	walk := inboxRowidWalk{next: 1}
	row, _, ok := walk.read(t.Context(), db)
	require.True(t, ok)
	require.Equal(t, "second", row.e.EventID, "an unverifiable seek cannot move the next position past readable rows")
	require.Equal(t, int64(2), row.seq)
	require.Equal(t, 1, walk.failed)
	require.ErrorIs(t, walk.resultError(), sql.ErrNoRows)
	require.Zero(t, db.Stats().InUse)
}

// routeSeekConn models damaged routing while leaving exact point reads valid.
type routeSeekConn struct {
	snapshotNativeConn
	backward bool
	seeks    int
}

func (c *routeSeekConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == salvageInboxSQL {
		c.seeks++
		if c.backward && c.seeks == 1 {
			args = []driver.NamedValue{{Ordinal: 1, Value: int64(1)}}
		} else if !c.backward && c.seeks > 1 {
			return nil, sql.ErrNoRows
		}
	}
	return c.snapshotNativeConn.QueryContext(ctx, query, args)
}

func TestInboxRowidWalk_ValidBackwardPoint_DoesNotRegress(t *testing.T) {
	db := routeSeekDB(t, true)
	walk := inboxRowidWalk{next: 2}
	row, _, ok := walk.read(t.Context(), db)
	require.False(t, ok, "a valid earlier row cannot make the walk move backward")
	require.Empty(t, row.e.EventID)
	require.Equal(t, 1, walk.failed)
	require.Contains(t, walk.resultError().Error(), "moved backward")
	require.Zero(t, db.Stats().InUse)
}

func TestCollectSalvageInboxRows_SeekOmitsReadableRow_ReportsDisagreement(t *testing.T) {
	db := routeSeekDB(t, false)
	rows, err := collectSalvageInboxRows(t.Context(), db)
	require.Len(t, rows, 2, "the readable table scan must survive a premature seek EOF")
	require.ErrorContains(t, err, "sequential scan and verified rowid walk disagree")
	require.ErrorContains(t, err, "lost record count unknown")
	require.Zero(t, db.Stats().InUse)
}

func routeSeekDB(t *testing.T, backward bool) *sql.DB {
	t.Helper()
	s := exchangeSnapshotStore(t)
	for _, id := range []string{"first", "second"} {
		_, err := s.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,'TEST','TEST-1','comment','{"to":"worker","body":"mail"}',0,0,'{}','sender','','2026-11-05T00:00:00Z')`, id)
		require.NoError(t, err)
	}
	db := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn {
		return &routeSeekConn{snapshotNativeConn: c, backward: backward}
	}})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}
