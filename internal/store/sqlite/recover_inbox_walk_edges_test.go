// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type edgeSeekConn struct {
	snapshotNativeConn
	cancel context.CancelFunc
}

func (c *edgeSeekConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if q == salvageInboxSQL {
		if c.cancel != nil {
			c.cancel()
			return nil, io.ErrUnexpectedEOF
		}
		// A valid point read confirms the injected maximum rowid, so the guard
		// must stop the next seek rather than wrap to a negative position.
		args = []driver.NamedValue{{Ordinal: 1, Value: int64(math.MaxInt64)}}
	}
	return c.snapshotNativeConn.QueryContext(ctx, q, args)
}

func TestInboxRowidWalk_MaximumSeek_DoesNotWrap(t *testing.T) {
	s := exchangeSnapshotStore(t)
	_, err := s.WriteDB().Exec(`INSERT INTO sync_events(rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,'last','TEST','TEST-1','comment','{"to":"worker","body":"last"}',0,0,'{}','sender','','2026-11-05T00:00:00Z')`, int64(math.MaxInt64))
	require.NoError(t, err)
	db := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn { return &edgeSeekConn{snapshotNativeConn: c} }})
	defer func() { require.NoError(t, db.Close()) }()
	walk := inboxRowidWalk{next: 1}
	row, _, ok := walk.read(t.Context(), db)
	require.True(t, ok)
	require.Equal(t, int64(math.MaxInt64), row.seq)
	require.True(t, walk.done, "the last possible rowid ends the walk")
	_, _, ok = walk.read(t.Context(), db)
	require.False(t, ok, "maximum rowid must not wrap and restart the walk")
	require.NoError(t, walk.resultError())
}

func TestInboxRowidWalk_CancelledSeek_StopsWithoutProbing(t *testing.T) {
	s := exchangeSnapshotStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	db := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn {
		return &edgeSeekConn{snapshotNativeConn: c, cancel: cancel}
	}})
	defer func() { require.NoError(t, db.Close()) }()
	walk := inboxRowidWalk{next: 1}
	_, _, ok := walk.read(ctx, db)
	require.False(t, ok)
	require.True(t, walk.done)
	require.Zero(t, walk.failed, "cancellation is not a damaged position to probe")
	require.Equal(t, int64(1), walk.next)
	require.ErrorIs(t, walk.resultError(), context.Canceled, "cancellation must survive the no-failed-probes path")
}
