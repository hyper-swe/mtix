// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.2.2.1.1: a replace lowers returned-seq marks above the surviving
// journal tail (registered and unregistered ids) and leaves lower marks and
// consumed wake budget accounting alone.
func TestWakeReplace_ReturnedMarks_LoweredOnlyAboveSurvivingTail(t *testing.T) {
	f := newWakeReplaceState(t, "above", false)
	ctx := t.Context()
	require.Greater(t, f.dropped, f.kept)
	require.NoError(t, f.store.AdvanceInboxReturnedMark(ctx, "worker", f.dropped))
	require.NoError(t, f.store.AdvanceInboxReturnedMark(ctx, "ghost", f.dropped))
	require.NoError(t, f.store.AdvanceInboxReturnedMark(ctx, "early", f.kept-1))
	budgets := wakeReplaceRows(t, f.raw, `SELECT * FROM wake_budget_counters ORDER BY agent_id,host_id,uid`, nil)

	_, err := f.store.Import(ctx, f.data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	for agent, want := range map[string]int64{"worker": f.kept, "ghost": f.kept, "early": f.kept - 1} {
		got, markErr := f.store.InboxReturnedMark(ctx, agent)
		require.NoError(t, markErr)
		assert.Equal(t, want, got, agent)
	}
	assert.Equal(t, budgets, wakeReplaceRows(t, f.raw, `SELECT * FROM wake_budget_counters ORDER BY agent_id,host_id,uid`, nil), "consumed budget accounting is never reset")
}

func TestWakeReplace_ReturnedMarkCleanupFailure_RollsBackMarks(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"registration column", `CREATE TRIGGER refuse_mark_trim BEFORE UPDATE ON agent_registrations WHEN NEW.returned_seq_mark <> OLD.returned_seq_mark BEGIN SELECT RAISE(ABORT,'forced mark cleanup failure'); END`},
		{"meta key", `CREATE TRIGGER refuse_mark_trim BEFORE UPDATE ON meta WHEN NEW.key LIKE 'inbox.returned_seq.%' BEGIN SELECT RAISE(ABORT,'forced mark cleanup failure'); END`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			require.NoError(t, f.store.AdvanceInboxReturnedMark(t.Context(), "worker", f.dropped))
			require.NoError(t, f.store.AdvanceInboxReturnedMark(t.Context(), "ghost", f.dropped))
			before := wakeReplaceAllState(t, f.raw)
			_, err := f.raw.Exec(tt.trigger)
			require.NoError(t, err)
			_, err = f.store.Import(t.Context(), f.data, sqlite.ImportModeReplace, false)
			require.ErrorContains(t, err, "forced mark cleanup failure")
			assert.Equal(t, before, wakeReplaceAllState(t, f.raw), "marks and journal are restored by rollback")
		})
	}
}
