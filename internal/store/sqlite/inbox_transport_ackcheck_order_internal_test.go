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

// openAckOrderConn returns one connection holding the production ack table with
// rows inserted in the given order, so rowid order is insertion order and
// differs from key order. reverse_unordered_selects makes any SELECT without
// an ORDER BY return its rows reversed, so a dropped ORDER BY is visible even on
// the index read, whose natural scan order already matches the keys.
func openAckOrderConn(t *testing.T, rows []ackRow, reverseUnordered bool) *sql.Conn {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	_, err = conn.ExecContext(ctx, `CREATE TABLE agent_inbox_ack (agent_id TEXT NOT NULL, event_seq INTEGER NOT NULL, acked_at TEXT NOT NULL, PRIMARY KEY (agent_id, event_seq))`)
	require.NoError(t, err)
	for _, r := range rows {
		_, err = conn.ExecContext(ctx, `INSERT INTO agent_inbox_ack (agent_id, event_seq, acked_at) VALUES (?, ?, '2026-11-05T00:00:00Z')`, r.agent, r.seq)
		require.NoError(t, err)
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf("PRAGMA reverse_unordered_selects = %t", reverseUnordered))
	require.NoError(t, err)
	return conn
}

// wantAckKeys is the rows in (agent_id, event_seq) order with the rowid each
// got from its insertion position.
func wantAckKeys(rows []ackRow) []ackKey {
	keys := make([]ackKey, len(rows))
	for i, r := range rows {
		keys[i] = ackKey{rowid: int64(i + 1), agent: r.agent, seq: r.seq}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].agent != keys[j].agent {
			return keys[i].agent < keys[j].agent
		}
		return keys[i].seq < keys[j].seq
	})
	return keys
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
				conn := openAckOrderConn(t, tt.rows, reverse)
				want := wantAckKeys(tt.rows)

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
