// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// oracleFields are the recorded columns the oracle watches on every task.
func oracleFields() map[string]string {
	return map[string]string{"status": "status", "assignee": "assignee", "defer_until": "defer_until",
		"deleted_at": "deleted_at", "priority": "priority", "agent_state": "agent_state", "parent": "parent_id"}
}

// installWriteOracle records, in a side table, every change SQLite itself sees
// to a recorded column of any task. It is independent of the recorder, so a
// write that reaches a task outside the recorder's scope (MTIX-95.31.19.2.6)
// leaves an oracle row with no history entry.
func (f *historyFixture) installWriteOracle(t *testing.T) {
	t.Helper()
	_, err := f.db.Exec(`CREATE TABLE zz_oracle (node_id TEXT, field TEXT)`)
	require.NoError(t, err)
	for field, col := range oracleFields() {
		// Test-only trigger over a fixed column list; no input is interpolated.
		stmt := fmt.Sprintf(`CREATE TRIGGER zz_oracle_%[1]s AFTER UPDATE ON nodes
			WHEN COALESCE(OLD.%[2]s, '') <> COALESCE(NEW.%[2]s, '')
			BEGIN INSERT INTO zz_oracle VALUES (NEW.id, '%[1]s'); END`, field, col)
		_, err := f.db.Exec(stmt)
		require.NoError(t, err)
	}
}

// assertOracleCovered fails for every change the oracle saw that has no
// history entry for that task and field.
func (f *historyFixture) assertOracleCovered(t *testing.T) {
	t.Helper()
	rows, err := f.db.Query(`SELECT DISTINCT o.node_id, o.field FROM zz_oracle o
		WHERE NOT EXISTS (SELECT 1 FROM task_history h JOIN nodes n ON COALESCE(NULLIF(n.uid,''), n.id) = h.task_uid
			WHERE n.id = o.node_id AND h.field = o.field)`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var id, field string
		require.NoError(t, rows.Scan(&id, &field))
		assert.Fail(t, "unrecorded change", "%s.%s changed without a history entry", id, field)
	}
	require.NoError(t, rows.Err())
}

func TestTaskHistory_WriteOracle_EveryRecordedColumnChangeHasAnEntry(t *testing.T) {
	ctx := context.Background()
	scenarios := []struct {
		name string
		run  func(t *testing.T, f *historyFixture)
	}{
		{"unblock cascade after a blocker resolves", func(t *testing.T, f *historyFixture) {
			f.link(t)
			require.NoError(t, f.s.TransitionStatus(ctx, ccRoot, model.StatusInProgress, "go", "bob"))
			require.NoError(t, f.s.TransitionStatus(ctx, ccRoot, model.StatusDone, "done", "bob"))
		}},
		{"cancel cascade unblocking a dependent", func(t *testing.T, f *historyFixture) {
			f.link(t)
			require.NoError(t, f.s.CancelNode(ctx, ccRoot, "gone", "bob", true))
		}},
		{"cascade cancel with a dependent inside the subtree", func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.AddDependency(ctx, &model.Dependency{FromID: ccChild, ToID: ccChild2, DepType: model.DepTypeBlocks, CreatedBy: "alice"}))
			require.NoError(t, f.s.CancelNode(ctx, ccRoot, "gone", "bob", true))
		}},
		{"dependency removal", func(t *testing.T, f *historyFixture) {
			f.link(t)
			require.NoError(t, f.s.RemoveDependency(ctx, ccRoot, ccOutside, model.DepTypeBlocks))
		}},
		{"refresh blocked", func(t *testing.T, f *historyFixture) {
			f.link(t)
			_, err := f.db.Exec(`DELETE FROM dependencies`)
			require.NoError(t, err)
			require.NoError(t, f.s.RefreshBlocked(ctx, ccOutside))
		}},
		{"delete and undelete cascade", func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.DeleteNode(ctx, ccRoot, true, "bob"))
			require.NoError(t, f.s.UndeleteNode(ctx, ccRoot))
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newHistoryFixture(t)
			seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen), ccNode(ccChild2, model.StatusOpen), ccNode(ccOutside, model.StatusOpen))
			f.installWriteOracle(t)
			f.clearHistory(t)
			sc.run(t, f)
			var changes int
			require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM zz_oracle`).Scan(&changes))
			require.Positive(t, changes, "the scenario must change recorded columns")
			f.assertOracleCovered(t)
		})
	}
}
