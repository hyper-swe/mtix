// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func assertBootstrapWakeMarks(t *testing.T, f wakeReplaceState, tail int64) {
	t.Helper()
	for _, host := range []string{"kept-host", "removed-host"} {
		var registration, lease, cursor int64
		require.NoError(t, f.raw.QueryRow(`SELECT high_water FROM agent_registrations WHERE host_id=?`, host).Scan(&registration))
		require.NoError(t, f.raw.QueryRow(`SELECT high_water FROM wake_leases WHERE host_id=?`, host).Scan(&lease))
		require.NoError(t, f.raw.QueryRow(`SELECT cursor FROM wake_scan_cursor WHERE host_id=?`, host).Scan(&cursor))
		assert.Equal(t, tail, registration)
		assert.Equal(t, tail, lease)
		assert.Equal(t, tail, cursor)
	}
}

func TestWakeBootstrap_SeedsExistingProgress_PreservesMetadata(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "history", true: "empty"}[empty], func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			if empty {
				require.NoError(t, sqlite.DiscardLocal(t.Context(), f.store, t.TempDir()))
			}
			metadata := wakeReplaceMetadata(t, f.raw)
			ledger := wakeReplaceRows(t, f.raw, `SELECT * FROM wake_ledger ORDER BY agent_id,host_id,uid,event_seq,epoch`, nil)
			tail, err := f.store.JournalTail(t.Context())
			require.NoError(t, err)
			require.NoError(t, f.store.InitHookScanFloorAtTail(t.Context()))
			floor, err := f.store.HookCursor(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tail, floor)
			assertBootstrapWakeMarks(t, f, tail)
			assert.Equal(t, metadata, wakeReplaceMetadata(t, f.raw))
			assert.Equal(t, ledger, wakeReplaceRows(t, f.raw, `SELECT * FROM wake_ledger ORDER BY agent_id,host_id,uid,event_seq,epoch`, nil))
		})
	}
}

func TestWakeBootstrap_WakeWriteFailure_RollsBackHookAndWakeFloors(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"cursor", `CREATE TRIGGER refuse_wake_seed BEFORE UPDATE ON wake_scan_cursor BEGIN SELECT RAISE(ABORT,'forced wake bootstrap failure'); END`},
		{"registration", `CREATE TRIGGER refuse_wake_seed BEFORE UPDATE ON agent_registrations BEGIN SELECT RAISE(ABORT,'forced wake bootstrap failure'); END`},
		{"lease", `CREATE TRIGGER refuse_wake_seed BEFORE UPDATE ON wake_leases BEGIN SELECT RAISE(ABORT,'forced wake bootstrap failure'); END`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			before := wakeReplaceAllState(t, f.raw)
			_, err := f.raw.Exec(tt.trigger)
			require.NoError(t, err)
			require.ErrorContains(t, f.store.InitHookScanFloorAtTail(t.Context()), "forced wake bootstrap failure")
			assert.Equal(t, before, wakeReplaceAllState(t, f.raw))
		})
	}
}

func TestWakeBootstrap_UsesWriterSnapshot_WhenReadPoolUnavailable(t *testing.T) {
	f := newWakeReplaceState(t, "above", false)
	tail, err := f.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.NoError(t, f.store.ReadDB().Close())
	require.NoError(t, f.store.InitHookScanFloorAtTail(t.Context()))
	for _, query := range []string{
		`SELECT cursor FROM wake_scan_cursor WHERE host_id='kept-host'`,
		`SELECT high_water FROM agent_registrations WHERE host_id='kept-host'`,
		`SELECT high_water FROM wake_leases WHERE host_id='kept-host'`,
	} {
		var mark int64
		require.NoError(t, f.store.WriteDB().QueryRow(query).Scan(&mark))
		assert.Equal(t, tail, mark)
	}
}

func TestWakeBootstrap_DoesNotLowerProgressAlreadyAboveTail(t *testing.T) {
	f := newWakeReplaceState(t, "above", false)
	mark := f.dropped + 5
	for _, stmt := range []string{
		// Exercise monotonic bootstrap semantics on preexisting scan progress.
		`UPDATE wake_scan_cursor SET cursor=?`,
		// Exercise monotonic registration progress without changing identity.
		`UPDATE agent_registrations SET high_water=?`,
		// Exercise monotonic lease progress without changing ownership or dirty.
		`UPDATE wake_leases SET high_water=?`,
	} {
		_, err := f.raw.Exec(stmt, mark)
		require.NoError(t, err)
	}
	metadata := wakeReplaceMetadata(t, f.raw)
	require.NoError(t, f.store.InitHookScanFloorAtTail(t.Context()))
	assertBootstrapWakeMarks(t, f, mark)
	assert.Equal(t, metadata, wakeReplaceMetadata(t, f.raw))
}
