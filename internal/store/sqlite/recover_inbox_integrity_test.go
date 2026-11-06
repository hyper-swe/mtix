// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type integrityFaultConn struct {
	snapshotNativeConn
	mode string
}

func (c *integrityFaultConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if q != `PRAGMA integrity_check(20)` {
		return c.snapshotNativeConn.QueryContext(ctx, q, args)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		return nil, fmt.Errorf("integrity check requires a deadline of at most five seconds")
	}
	switch c.mode {
	case "query":
		return nil, io.ErrUnexpectedEOF
	case "timeout":
		<-ctx.Done()
		return nil, ctx.Err()
	default:
		return &integrityFaultRows{mode: c.mode}, nil
	}
}

type integrityFaultRows struct {
	mode string
	done bool
}

func (r *integrityFaultRows) Columns() []string { return []string{"integrity_check"} }
func (r *integrityFaultRows) Close() error      { return nil }
func (r *integrityFaultRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	switch r.mode {
	case "empty":
		return io.EOF
	case "iteration":
		return io.ErrUnexpectedEOF
	case "scan":
		values[0] = nil
	case "damage":
		values[0] = "page referenced twice"
	default:
		values[0] = "ok"
	}
	return nil
}

func TestCheckInboxIntegrity_UnknownOrDamaged_IsNotClean(t *testing.T) {
	for _, mode := range []string{"query", "timeout", "empty", "iteration", "scan", "damage", "ok"} {
		t.Run(mode, func(t *testing.T) {
			s := exchangeSnapshotStore(t)
			db := sql.OpenDB(&snapshotConnector{path: s.dbPath, afterClose: func() error { return nil }, wrap: func(c snapshotNativeConn) snapshotNativeConn {
				return &integrityFaultConn{snapshotNativeConn: c, mode: mode}
			}})
			defer func() { require.NoError(t, db.Close()) }()
			ctx, cancel := context.WithCancel(t.Context())
			if mode == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			}
			defer cancel()
			err := checkInboxIntegrity(ctx, db)
			if mode == "ok" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "integrity_check")
			}
			if mode == "timeout" {
				require.True(t, errors.Is(err, context.DeadlineExceeded))
			}
			require.Zero(t, db.Stats().InUse)
		})
	}
}

func TestSalvageInboxAck_FullIdentityMismatch_LeavesUnread(t *testing.T) {
	for _, field := range []string{"event", "project", "node", "uid", "author", "payload", "created", "op"} {
		t.Run(field, func(t *testing.T) {
			db := routeSeekDB(t, true)
			// Obtain the real row before changing the claimed identity; the coverage is genuine.
			walk := inboxRowidWalk{next: 1}
			row, _, ok := walk.read(t.Context(), db)
			require.True(t, ok)
			_, err := db.Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('worker',100)`)
			require.NoError(t, err)
			switch field {
			case "event":
				row.e.EventID = "other"
			case "project":
				row.e.Project = "OTHER"
			case "node":
				row.e.NodeID = "TEST-2"
			case "uid":
				row.e.UID = "other"
			case "author":
				row.e.Author = "other"
			case "payload":
				row.e.Payload = `{"to":"worker","body":"other"}`
			case "created":
				row.e.CreatedAt = "2026-11-06T00:00:00Z"
			case "op":
				_, err = db.Exec(`UPDATE sync_events SET op_type='update_field' WHERE rowid=?`, row.seq)
				require.NoError(t, err)
			}
			require.ErrorContains(t, salvageInboxAck(t.Context(), db, &row.e, row.seq), "identity")
			require.False(t, row.e.Acked)
			require.Empty(t, row.e.AckedAt)
		})
	}
}
