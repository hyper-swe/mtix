// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskHistory_StatusRepair_RecordsTheRepairedTaskAndItsUnblockedDependent:
// the repair writes the repaired task's status and unblocks the dependent; both
// are system changes, the repairer is the actor, and nothing else is recorded.
func TestTaskHistory_StatusRepair_RecordsTheRepairedTaskAndItsUnblockedDependent(t *testing.T) {
	s, raw := repairFixture(t)
	s.SetClock(func() time.Time { return time.Now().UTC().Add(time.Hour) })
	_, err := raw.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)

	_, applied, err := s.RepairNodeStatus(context.Background(), "MTIX-1.1", "repairer", false)
	require.NoError(t, err)
	require.True(t, applied)

	type change struct{ old, new, origin, actor string }
	read := func(id string) []change {
		rows, qerr := raw.Query(`SELECT h.old_value, h.new_value, h.origin, h.actor FROM task_history h
			JOIN nodes n ON COALESCE(NULLIF(n.uid, ''), n.id) = h.task_uid WHERE n.id = ? AND h.field = 'status'`, id)
		require.NoError(t, qerr)
		defer func() { require.NoError(t, rows.Close()) }()
		var out []change
		for rows.Next() {
			var c change
			require.NoError(t, rows.Scan(&c.old, &c.new, &c.origin, &c.actor))
			out = append(out, c)
		}
		require.NoError(t, rows.Err())
		return out
	}
	assert.Equal(t, []change{{`"in_progress"`, `"done"`, "system", "repairer"}}, read("MTIX-1.1"))
	assert.Equal(t, []change{{`"blocked"`, `"open"`, "system", "repairer"}}, read("MTIX-2"), "the dependent the repair unblocks")
}
