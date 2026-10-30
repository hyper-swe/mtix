// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Resolution receipts from peers cannot plant values outside ordinary retention.
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const plantedResolutionValue = "untrusted-value-must-not-be-retained"

type resolutionImportCase struct {
	name, field     string
	old, new, valid bool
}

func resolutionImportFile(t *testing.T, c resolutionImportCase, orphan bool) (*sqlite.ExportData, string, string) {
	t.Helper()
	_, file := historyFile(t)
	e := &file.Nodes[0].History[0]
	e.Kind, e.Field, e.Old, e.New = "resolution", c.field, nil, nil
	value := plantedResolutionValue
	if c.old {
		e.Old = &value
	}
	if c.new {
		e.New = &value
	}
	if orphan {
		moved := *e
		moved.TaskUID = "orphaned-receipt-uid"
		file.Nodes[0].History = nil
		file.History = append(file.History, moved)
		e = &file.History[len(file.History)-1]
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	return file, e.TaskUID, e.ID
}

func checkResolutionImport(t *testing.T, c resolutionImportCase, mode sqlite.ImportMode, orphan bool) {
	t.Helper()
	file, uid, id := resolutionImportFile(t, c, orphan)
	dst, _ := goldenStore(t)
	_, err := dst.s.Import(context.Background(), file, mode, true)
	if c.valid {
		require.NoError(t, err)
		return
	}
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), uid, "name the task without repeating its value")
	assert.Contains(t, err.Error(), id, "name the bad entry")
	assert.NotContains(t, err.Error(), plantedResolutionValue)
	var rows int
	require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history`).Scan(&rows))
	assert.Zero(t, rows, "validation precedes every write")
}

func TestHistoryResolution_ImportValidatesValueFreeSelector(t *testing.T) {
	cases := []resolutionImportCase{
		{"good ours", "resolution:ours:assignee", false, false, true},
		{"good multiple", "resolution:theirs:status,assignee,agent_state,defer_until,deleted_at", false, false, true},
		{"old value", "resolution:ours:assignee", true, false, false},
		{"new value", "resolution:ours:assignee", false, true, false},
		{"both values", "resolution:ours:assignee", true, true, false},
		{"wrong prefix", "choice:ours:assignee", false, false, false},
		{"wrong side", "resolution:other:assignee", false, false, false},
		{"empty fields", "resolution:ours:", false, false, false},
		{"unknown field", "resolution:ours:description", false, false, false},
		{"duplicate field", "resolution:ours:status,status", false, false, false},
		{"extra delimiter", "resolution:ours:status:assignee", false, false, false},
	}
	for _, mode := range bothModes {
		for _, orphan := range []bool{false, true} {
			for _, c := range cases {
				t.Run(string(mode)+"/"+map[bool]string{false: "node", true: "orphan"}[orphan]+"/"+c.name, func(t *testing.T) { checkResolutionImport(t, c, mode, orphan) })
			}
		}
	}
}

func TestHistoryResolution_RetentionKeepsOnlyValueFreeReceipts(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(map[bool]string{false: "planted values rejected", true: "valid receipt survives"}[valid], func(t *testing.T) {
			c := resolutionImportCase{field: "resolution:ours:assignee", old: !valid, new: !valid, valid: valid}
			file, uid, id := resolutionImportFile(t, c, false)
			dst, _ := goldenStore(t)
			_, err := dst.s.Import(context.Background(), file, sqlite.ImportModeMerge, true)
			if valid {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, model.ErrInvalidInput)
			}
			dst.s.SetHistoryLimitSource(func() int { return 1 })
			for _, priority := range []model.Priority{model.PriorityHigh, model.PriorityLow, model.PriorityMedium} {
				require.NoError(t, dst.s.UpdateNode(context.Background(), file.Nodes[0].ID, &store.NodeUpdate{Priority: &priority}))
			}
			var changes, trims, receipts, valued int
			require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND kind = 'change'`, uid).Scan(&changes))
			require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND kind = 'retention_drop'`, uid).Scan(&trims))
			require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE entry_id = ?`, id).Scan(&receipts))
			require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE kind = 'resolution' AND (old_value IS NOT NULL OR new_value IS NOT NULL)`).Scan(&valued))
			assert.LessOrEqual(t, changes, 1)
			assert.Positive(t, trims, "the test must actually trim ordinary changes")
			assert.Equal(t, map[bool]int{false: 0, true: 1}[valid], receipts)
			assert.Zero(t, valued, "a planted value must never become a permanent administrative row")
		})
	}
}
