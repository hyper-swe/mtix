// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests transaction-local sender attribution, legacy events and read failures.
package sqlite

import (
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeBudgetSender_Attribution(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "sender.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	for _, tc := range []struct {
		name          string
		op            model.OpType
		payload, want string
	}{
		{"payload author", model.OpComment, `{"to":"worker","author_id":"payload-author"}`, "payload-author"},
		{"envelope fallback", model.OpComment, `{"to":"worker"}`, "envelope-author"},
		{"other addressee", model.OpComment, `{"to":"other","author_id":"payload-author"}`, "payload-author"},
		{"malformed", model.OpComment, `{`, "envelope-author"},
		{"non-comment", model.OpCreateNode, `{}`, "envelope-author"},
		{"non-comment payload author", model.OpCreateNode, `{"author_id":"payload-author"}`, "payload-author"},
		{"missing addressee", model.OpComment, `{}`, "envelope-author"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := s.WriteDB().BeginTx(t.Context(), nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback()) }()
			_, err = tx.ExecContext(t.Context(), `INSERT INTO sync_events
    (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
    VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "sender-event", "TEST", "TEST-1", tc.op, tc.payload, 1, 1, "{}", "envelope-author", "0123456789abcdef", "2026-10-28T00:00:00Z")
			require.NoError(t, err)
			var seq int64
			require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT rowid FROM sync_events WHERE event_id=?", "sender-event").Scan(&seq))
			got, err := wakeBudgetSender(t.Context(), tx, model.WakeKey{AgentID: "worker", EventSeq: seq})
			require.Equal(t, tc.want, got)
			require.NoError(t, err)
		})
	}
	tx, err := s.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(t, err)
	got, err := wakeBudgetSender(t.Context(), tx, model.WakeKey{EventSeq: 999})
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, tx.Rollback())
	_, err = wakeBudgetSender(t.Context(), tx, model.WakeKey{EventSeq: 999})
	require.ErrorIs(t, err, sql.ErrTxDone)
}
