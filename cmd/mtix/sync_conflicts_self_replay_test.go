// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Self-replay conflict rows (MTIX-216). Before 0.5.4 a pull replayed this
// store's own pushed events as if remote, and every own event older than a
// newer write to the same field was logged as an lww conflict. 0.5.4 stopped
// writing them (MTIX-95.2) but still counted the old rows as unresolved.
// A row is self-replay when BOTH its winner and loser event ids are in this
// store's sync_events with sync_status other than applied (emitted here).

// seedLocalEvent inserts a sync_events row with the given id and status.
func seedLocalEvent(t *testing.T, id, status string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sync_events
			  (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts,
			   lamport_clock, vector_clock, author_id, author_machine_hash, sync_status, created_at)
			VALUES (?, 'TEST', 'TEST-1', 'update_field', '{"field_name":"title"}', 1,
			        1, '{}', 'agent', 'machinehash', ?, '2026-09-25T00:00:00Z')`, id, status)
		return err
	}))
}

// seedPairConflict inserts a conflict row naming the two event ids and
// returns its id.
func seedPairConflict(t *testing.T, node, field, resolution, winner, loser string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sync_conflicts
			  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at)
			VALUES (?, ?, ?, ?, ?, '2026-09-25T00:00:00Z')`,
			winner, loser, node, nullIfEmpty(field), resolution)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	}))
	return id
}

type listedConflict struct {
	ConflictID int64 `json:"conflict_id"`
	Unresolved bool  `json:"unresolved"`
	SelfReplay *bool `json:"self_replay"`
}

// listAllConflicts returns every row of conflicts list --all --json.
func listAllConflicts(t *testing.T) []listedConflict {
	t.Helper()
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	stdout, stderr, err := execSyncCLI(context.Background(), []string{"sync", "conflicts", "list", "--all"})
	require.NoError(t, err, stderr)
	var rows []listedConflict
	require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
	return rows
}

func openConflictCount(t *testing.T) string {
	t.Helper()
	return string(statusJSON(t)["open_conflicts"])
}

// TestSyncConflicts_PreFiveFourSelfReplayRows_NotUnresolved: the rows an
// 0.5.3 own-event replay wrote (a duplicate pair per replay; winner a newer
// pending event, loser a pushed one, both emitted here) are not counted,
// not listed by default, and are tagged self_replay in --all and --json.
func TestSyncConflicts_PreFiveFourSelfReplayRows_NotUnresolved(t *testing.T) {
	initTestApp(t)
	seedLocalEvent(t, "evt-new", "pending")
	seedLocalEvent(t, "evt-old", "pushed")
	a := seedPairConflict(t, "TEST-1", "title", "lww", "evt-new", "evt-old")
	b := seedPairConflict(t, "TEST-1", "title", "lww", "evt-new", "evt-old")

	require.JSONEq(t, "0", openConflictCount(t), "self-replay rows are not unresolved conflicts")
	require.Regexp(t, regexp.MustCompile(`(?m)^unresolved conflicts\s+0$`), statusText(t))
	ids, _ := listConflicts(t, false)
	require.Empty(t, ids, "the default list omits self-replay rows")

	rows := listAllConflicts(t)
	require.Len(t, rows, 2)
	for i, want := range []int64{a, b} {
		require.Equal(t, want, rows[i].ConflictID)
		require.False(t, rows[i].Unresolved)
		require.NotNil(t, rows[i].SelfReplay, "--json carries self_replay")
		require.True(t, *rows[i].SelfReplay)
	}
}

// TestSyncConflicts_SelfReplayRule_OnlyBothLocalCounts: only a pair of two
// locally emitted events is self-replay. A pair with an applied (mirrored
// foreign) event, or with an event absent from sync_events (pruned or
// compacted), stays unresolved: the safe direction.
func TestSyncConflicts_SelfReplayRule_OnlyBothLocalCounts(t *testing.T) {
	tests := []struct {
		name          string
		events        map[string]string // event id -> sync_status; absent ids are not seeded
		winner, loser string
		wantSelf      bool
	}{
		{"pending and pushed", map[string]string{"w": "pending", "l": "pushed"}, "w", "l", true},
		{"conflicted counts as local", map[string]string{"w": "conflicted", "l": "pushed"}, "w", "l", true},
		{"loser applied", map[string]string{"w": "pushed", "l": "applied"}, "w", "l", false},
		{"winner applied", map[string]string{"w": "applied", "l": "pushed"}, "w", "l", false},
		{"both applied", map[string]string{"w": "applied", "l": "applied"}, "w", "l", false},
		{"loser pruned", map[string]string{"w": "pushed"}, "w", "l", false},
		{"winner pruned", map[string]string{"l": "pushed"}, "w", "l", false},
		{"both pruned", map[string]string{}, "w", "l", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			for id, status := range tt.events {
				seedLocalEvent(t, id, status)
			}
			id := seedPairConflict(t, "TEST-1", "title", "lww", tt.winner, tt.loser)

			rows := listAllConflicts(t)
			require.Len(t, rows, 1)
			require.Equal(t, id, rows[0].ConflictID)
			require.NotNil(t, rows[0].SelfReplay)
			require.Equal(t, tt.wantSelf, *rows[0].SelfReplay)
			require.Equal(t, !tt.wantSelf, rows[0].Unresolved)
			want := "1"
			if tt.wantSelf {
				want = "0"
			}
			require.JSONEq(t, want, openConflictCount(t))
		})
	}
}

// TestSyncConflicts_SelfReplayRow_NeverMasksOrUnmasksGenuine: a self-replay
// row takes no part in the unresolved computation. A genuine conflict (a
// pair with a foreign event) stays unresolved with a self-replay row before
// or after it on the same node and field, and a manual decision on the
// genuine conflict is not undone by a later self-replay row.
func TestSyncConflicts_SelfReplayRow_NeverMasksOrUnmasksGenuine(t *testing.T) {
	setup := func(t *testing.T) {
		t.Helper()
		initTestApp(t)
		seedLocalEvent(t, "own-a", "pending")
		seedLocalEvent(t, "own-b", "pushed")
		seedLocalEvent(t, "foreign", "applied")
	}
	t.Run("self-replay row after a genuine conflict", func(t *testing.T) {
		setup(t)
		genuine := seedPairConflict(t, "TEST-1", "title", "lww", "foreign", "own-b")
		seedPairConflict(t, "TEST-1", "title", "lww", "own-a", "own-b")
		ids, _ := listConflicts(t, false)
		require.Equal(t, []int64{genuine}, ids)
		require.JSONEq(t, "1", openConflictCount(t))
	})
	t.Run("self-replay row before a genuine conflict", func(t *testing.T) {
		setup(t)
		seedPairConflict(t, "TEST-1", "title", "lww", "own-a", "own-b")
		genuine := seedPairConflict(t, "TEST-1", "title", "lww", "foreign", "own-b")
		ids, _ := listConflicts(t, false)
		require.Equal(t, []int64{genuine}, ids)
		require.JSONEq(t, "1", openConflictCount(t))
	})
	t.Run("a resolved genuine conflict stays resolved after a later self-replay row", func(t *testing.T) {
		setup(t)
		genuine := seedPairConflict(t, "TEST-1", "title", "lww", "foreign", "own-b")
		resolveConflict(t, genuine, "acknowledge")
		seedPairConflict(t, "TEST-1", "title", "lww", "own-a", "own-b")
		ids, _ := listConflicts(t, false)
		require.Empty(t, ids)
		require.JSONEq(t, "0", openConflictCount(t))
	})
}

// TestSyncStatus_ManySelfReplayRows_NoBanner: 51 self-replay rows count 0
// and raise no >50 banner.
func TestSyncStatus_ManySelfReplayRows_NoBanner(t *testing.T) {
	initTestApp(t)
	seedLocalEvent(t, "evt-new", "pending")
	seedLocalEvent(t, "evt-old", "pushed")
	for i := 0; i < 51; i++ {
		seedPairConflict(t, fmt.Sprintf("TEST-%d", i+1), "title", "lww", "evt-new", "evt-old")
	}
	got := statusJSON(t)
	require.JSONEq(t, "0", string(got["open_conflicts"]))
	require.JSONEq(t, "false", string(got["high_conflict"]))
	require.NotContains(t, statusText(t), "WARN")
}

// TestRunSyncDoctor_SelfReplayConflictRows_InformationalCheck: doctor has a
// "self-replay conflict rows" check that always passes. With none it says
// so; with some it names the count, explains they are not counted, and
// gives the fix: none required, inspect with conflicts list --all. Text and
// --json carry the same detail and fix.
func TestRunSyncDoctor_SelfReplayConflictRows_InformationalCheck(t *testing.T) {
	const fix = "none required; inspect with mtix sync conflicts list --all"
	tests := []struct {
		name       string
		rows       int
		wantDetail string
	}{
		{"none", 0, "no self-replay conflict rows"},
		{"some", 3, "3 conflict rows"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			seedLocalEvent(t, "evt-new", "pending")
			seedLocalEvent(t, "evt-old", "pushed")
			for i := 0; i < tt.rows; i++ {
				seedPairConflict(t, "TEST-1", "title", "lww", "evt-new", "evt-old")
			}
			ctx := context.Background()

			var out bytes.Buffer
			app.jsonOutput = true
			_ = runSyncDoctor(ctx, &out, nil, transport.Options{})
			check := doctorCheckFromJSON(t, out.Bytes(), "self-replay conflict rows")
			require.True(t, check.Pass, check.Detail)
			require.False(t, check.Warn)
			require.Contains(t, check.Detail, tt.wantDetail)
			if tt.rows > 0 {
				require.Contains(t, check.Detail, "not counted")
				require.Equal(t, fix, check.Fix)
			}

			out.Reset()
			app.jsonOutput = false
			_ = runSyncDoctor(ctx, &out, nil, transport.Options{})
			require.Regexp(t, regexp.MustCompile(`(?m)^\[PASS\] self-replay conflict rows\s+\S`), out.String())
			if tt.rows > 0 {
				require.Contains(t, out.String(), "fix: "+fix)
			}
		})
	}
}

// TestAppendSelfReplayConflictCheck_NoStore_Fails: without a local store
// the check fails like the other local checks.
func TestAppendSelfReplayConflictCheck_NoStore_Fails(t *testing.T) {
	r := appendSelfReplayConflictCheck(context.Background(), DoctorReport{OverallPass: true}, nil)
	require.False(t, r.OverallPass)
	require.Equal(t, []DoctorCheck{{Name: "self-replay conflict rows", Pass: false, Detail: "local store not initialized"}}, r.Checks)
}

// TestSyncConflictsResolve_SelfReplayRow_TakesNoPartInResolveCheck: a later
// self-replay row neither blocks the resolve of a genuine conflict nor
// sends the user to resolve it instead, and resolving a self-replay row is
// refused (its decision would close earlier genuine conflicts unreviewed).
func TestSyncConflictsResolve_SelfReplayRow_TakesNoPartInResolveCheck(t *testing.T) {
	initTestApp(t)
	seedLocalEvent(t, "own-a", "pending")
	seedLocalEvent(t, "own-b", "pushed")
	seedLocalEvent(t, "foreign", "applied")
	genuine := seedPairConflict(t, "TEST-1", "title", "lww", "foreign", "own-b")
	selfReplay := seedPairConflict(t, "TEST-1", "title", "lww", "own-a", "own-b")

	var stdout, stderr bytes.Buffer
	err := runSyncConflictsResolve(context.Background(), &stdout, &stderr,
		fmt.Sprint(selfReplay), "acknowledge")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Contains(t, err.Error(), "self-replay row")
	require.JSONEq(t, "1", openConflictCount(t), "the genuine conflict is still unresolved")

	resolveConflict(t, genuine, "acknowledge")
	require.JSONEq(t, "0", openConflictCount(t))
}
