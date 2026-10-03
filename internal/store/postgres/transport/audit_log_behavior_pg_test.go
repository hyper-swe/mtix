// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestAuditLog_RowTriggersRefuseUpdateAndDelete: migration 006 binds
// audit_log_immutable to BEFORE UPDATE and BEFORE DELETE row triggers on
// audit_log, so both statements raise even for the table owner, and the
// row survives (FR-18.5). A row only exists because the test inserts it:
// mtix writes none (see TestAuditLog_PushWritesNoRows).
func TestAuditLog_RowTriggersRefuseUpdateAndDelete(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	_, err := pool.Inner().Exec(ctx,
		`INSERT INTO audit_log (project_prefix, actor, action) VALUES ('TST', 'tester', 'seed')`)
	require.NoError(t, err)

	tests := []struct {
		name string
		stmt string
	}{
		{"update", `UPDATE audit_log SET actor = 'forged' WHERE project_prefix = 'TST'`},
		{"delete", `DELETE FROM audit_log WHERE project_prefix = 'TST'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Inner().Exec(ctx, tt.stmt)
			require.Error(t, err)
			require.Contains(t, err.Error(), "append-only", "the trigger raises the FR-18.5 exception")
		})
	}
	var actor string
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT actor FROM audit_log WHERE project_prefix = 'TST'`).Scan(&actor))
	require.Equal(t, "tester", actor, "the refused statements left the row unchanged")
}

// TestAuditLog_PushWritesNoRows pins the documented absence: a push that
// inserts hub events writes nothing to audit_log (the sync_events rows land). The
// hub's record of mutations is sync_events and sync_conflicts; audit_log is
// an append-only table the schema provides and mtix itself never fills
// (docs/SECURITY-MODEL.md, "What the audit trail is").
func TestAuditLog_PushWritesNoRows(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	batch := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-000000000001", "MTIX-1", "alice", 1),
		makeEvent("0193fa00-0000-7000-8000-000000000002", "MTIX-2", "alice", 2),
	}
	ids, _, err := pool.PushEvents(ctx, batch)
	require.NoError(t, err)
	require.Len(t, ids, len(batch), "the push landed on the hub")
	var audit int
	require.NoError(t, pool.Inner().QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&audit))
	require.Zero(t, audit, "no push writes an audit_log row")
}
