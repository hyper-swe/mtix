// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

type ackRow struct {
	agent string
	seq   int64
}

// openAckOrderConn returns one connection whose ack table is built from the
// production schema (schemaSQL), with rows inserted in the given order so rowid
// order is insertion order and differs from key order. It returns the expected
// keys in (agent_id, event_seq) order, carrying the rowid SQLite actually
// assigned to each insert. reverse_unordered_selects makes any SELECT without
// an ORDER BY return its rows reversed, so a dropped ORDER BY is visible even on
// the index read, whose natural scan order already matches the keys; the
// pragma is read back and its effect observed before the connection is used.
func openAckOrderConn(t *testing.T, rows []ackRow, reverseUnordered bool) (*sql.Conn, []ackKey) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.ExecContext(ctx, schemaSQL)
	require.NoError(t, err)

	keys := make([]ackKey, 0, len(rows))
	for _, r := range rows {
		res, err := conn.ExecContext(ctx, `INSERT INTO agent_inbox_ack (agent_id, event_seq, acked_at) VALUES (?, ?, '2026-11-05T00:00:00Z')`, r.agent, r.seq)
		require.NoError(t, err)
		rowid, err := res.LastInsertId()
		require.NoError(t, err)
		require.Positive(t, rowid, "production ack table must be a rowid table")
		keys = append(keys, ackKey{rowid: rowid, agent: r.agent, seq: r.seq})
	}

	_, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA reverse_unordered_selects = %t", reverseUnordered))
	require.NoError(t, err)
	requireReverseUnorderedInEffect(t, conn, reverseUnordered)

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].agent != keys[j].agent {
			return keys[i].agent < keys[j].agent
		}
		return keys[i].seq < keys[j].seq
	})
	return conn, keys
}

// requireReverseUnorderedInEffect fails unless the pragma reads back as set and
// an unordered table scan actually comes back in the matching rowid direction.
// Without this, a driver that ignored the pragma would let a dropped ORDER BY
// on the index read go unnoticed.
func requireReverseUnorderedInEffect(t *testing.T, conn *sql.Conn, want bool) {
	t.Helper()
	ctx := context.Background()
	var got int
	require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA reverse_unordered_selects").Scan(&got))
	require.Equal(t, want, got == 1, "reverse_unordered_selects read back as %d", got)

	rows, err := conn.QueryContext(ctx, "SELECT rowid FROM agent_inbox_ack NOT INDEXED")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var rowids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		rowids = append(rowids, id)
	}
	require.NoError(t, rows.Err())
	require.Greater(t, len(rowids), 1, "need several rows to observe scan direction")
	for i := 1; i < len(rowids); i++ {
		if want {
			require.Less(t, rowids[i], rowids[i-1], "unordered scan must be descending with the pragma on: %v", rowids)
		} else {
			require.Greater(t, rowids[i], rowids[i-1], "unordered scan must be ascending with the pragma off: %v", rowids)
		}
	}
}

// FR-19.1 / MTIX-112.49: both check reads return keys in (agent_id, event_seq)
// order across several agents and rows, and a clean multi-agent store passes.
// Dropping ORDER BY from either read fails these cases.
func TestCheckAckIndex_MultiAgentCleanStore_KeyOrderedAndAgrees(t *testing.T) {
	tests := []struct {
		name string
		rows []ackRow
	}{
		{"two agents interleaved", []ackRow{
			{"beta", 1}, {"alpha", 1}, {"beta", 2}, {"alpha", 2},
		}},
		{"three agents reverse key order", []ackRow{
			{"gamma", 30}, {"gamma", 20}, {"beta", 30}, {"beta", 10},
			{"alpha", 40}, {"alpha", 5},
		}},
		{"shared seqs across agents", []ackRow{
			{"carol", 7}, {"alice", 7}, {"bob", 7}, {"alice", 3},
			{"carol", 3}, {"bob", 9}, {"alice", 9},
		}},
		{"many rows per agent", []ackRow{
			{"zed", 100}, {"agent-1", 250}, {"zed", 1}, {"agent-1", 2},
			{"agent-10", 50}, {"agent-2", 50}, {"zed", 50}, {"agent-1", 251},
			{"agent-2", 1}, {"agent-10", 49},
		}},
	}
	for _, tt := range tests {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse_unordered=%t", tt.name, reverse), func(t *testing.T) {
				ctx := context.Background()
				conn, want := openAckOrderConn(t, tt.rows, reverse)

				viaIndex, err := readAckKeys(ctx, conn, ackKeysViaIndexSQL)
				require.NoError(t, err)
				require.Equal(t, want, viaIndex, "index read must be in key order")

				viaTable, err := readAckKeys(ctx, conn, ackKeysViaTableSQL)
				require.NoError(t, err)
				require.Equal(t, want, viaTable, "table read must be in key order")

				require.NoError(t, checkAckIndexAgreesWithTable(ctx, conn))
			})
		}
	}
}
