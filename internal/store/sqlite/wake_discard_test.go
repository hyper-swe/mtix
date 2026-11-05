// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestWakeDiscardLocal_ResetsOnlyJournalProgress_PreservesIdentityAndBudgets(t *testing.T) {
	for _, floor := range []string{"above", "below", "tail", "absent"} {
		t.Run(floor, func(t *testing.T) {
			f := newWakeReplaceState(t, floor, false)
			metadata := wakeReplaceMetadata(t, f.raw)
			dir := t.TempDir()
			for range 2 {
				require.NoError(t, sqlite.DiscardLocal(t.Context(), f.store, dir))
				tail, err := f.store.JournalTail(t.Context())
				require.NoError(t, err)
				require.Zero(t, tail)
				stats, err := f.store.WakeLedgerStats(t.Context())
				require.NoError(t, err)
				assert.Zero(t, stats.Rows, "no claim or terminal outlives the wiped journal")
				assertReplaceWakeMarks(t, f, 0)
				assert.Equal(t, metadata, wakeReplaceMetadata(t, f.raw), "holds, epochs, ownership and consumed budget remain unchanged")
			}
		})
	}
}

func TestWakeDiscardLocal_CleanupFailure_RollsBackAllState(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"ledger", `CREATE TRIGGER refuse_wake_trim BEFORE DELETE ON wake_ledger BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
		{"cursor", `CREATE TRIGGER refuse_wake_trim BEFORE UPDATE ON wake_scan_cursor BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
		{"registration", `CREATE TRIGGER refuse_wake_trim BEFORE UPDATE ON agent_registrations BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
		{"lease", `CREATE TRIGGER refuse_wake_trim BEFORE UPDATE ON wake_leases BEGIN SELECT RAISE(ABORT,'forced wake cleanup failure'); END`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			require.NoError(t, f.store.InboxAckThrough(t.Context(), "worker", f.dropped))
			_, err := f.raw.Exec(`INSERT INTO hook_dispatch_cursor(id,cursor) VALUES(1,?)`, f.dropped)
			require.NoError(t, err)
			_, err = f.store.ClaimHookDispatch(t.Context(), "discard-hook", f.dropped, time.Minute)
			require.NoError(t, err)
			before := wakeReplaceAllState(t, f.raw)
			board, err := f.store.Export(t.Context(), "", "")
			require.NoError(t, err)
			_, err = f.raw.Exec(tt.trigger)
			require.NoError(t, err)
			err = sqlite.DiscardLocal(t.Context(), f.store, t.TempDir())
			require.ErrorContains(t, err, "forced wake cleanup failure")
			assert.Equal(t, before, wakeReplaceAllState(t, f.raw))
			after, err := f.store.Export(t.Context(), "", "")
			require.NoError(t, err)
			assert.Equal(t, board.Nodes, after.Nodes)
			assert.Equal(t, board.Dependencies, after.Dependencies)
		})
	}
}
