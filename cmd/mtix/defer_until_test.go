// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-95.22: `mtix defer <id> --until <ts>` validated the timestamp and then
// dropped it, so defer_until was never stored and a deferred node never woke
// (FR-3.8b). The defer also recorded a hard-coded "cli" author instead of the
// process identity (MTIX-24).

// rawDeferUntil reads the stored defer_until column verbatim, so a test sees
// the exact text the wake query compares against.
func rawDeferUntil(t *testing.T, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT defer_until FROM nodes WHERE id = ?`, id).Scan(&v))
	return v
}

// nodeEventOps returns the op_type of every sync event of a node after its
// create event, oldest first.
func nodeEventOps(t *testing.T, id string) []model.OpType {
	t.Helper()
	rows, err := app.store.Query(context.Background(),
		`SELECT op_type FROM sync_events
		 WHERE node_id = ? AND op_type != ? ORDER BY lamport_clock`,
		id, string(model.OpCreateNode))
	require.NoError(t, err)
	defer rows.Close()
	var ops []model.OpType
	for rows.Next() {
		var op string
		require.NoError(t, rows.Scan(&op))
		ops = append(ops, model.OpType(op))
	}
	require.NoError(t, rows.Err())
	return ops
}

// TestDefer_WithUntil_StoresDeferUntil verifies the CLI path stores the wake
// time as UTC RFC 3339 (MTIX-95.22, FR-3.8b).
func TestDefer_WithUntil_StoresDeferUntil(t *testing.T) {
	tests := []struct {
		name  string
		until string
		want  string
	}{
		{"utc timestamp", "2027-01-01T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"offset timestamp is stored in utc", "2027-01-01T05:30:00+05:30", "2027-01-01T00:00:00Z"},
		{"fractional seconds are dropped", "2027-01-01T00:00:00.750Z", "2027-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Defer Timed", "", "", 3, "", "", "", "", ""))

			require.NoError(t, runDefer("TEST-1", tt.until))

			raw := rawDeferUntil(t, "TEST-1")
			require.True(t, raw.Valid, "defer_until must be stored, not dropped")
			assert.Equal(t, tt.want, raw.String)

			node, err := app.store.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, model.StatusDeferred, node.Status)
		})
	}
}

// TestDefer_RecordsProcessAuthor verifies the CLI defer records the process
// author identity (MTIX_AUTHOR_ID > author_id config > "cli", MTIX-24) on
// both the activity entry and the sync event, not a hard-coded "cli".
func TestDefer_RecordsProcessAuthor(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "agent-7")
	initTestApp(t)
	require.Equal(t, "agent-7", app.authorID)
	require.NoError(t, runCreate("Defer Author", "", "", 3, "", "", "", "", ""))

	require.NoError(t, runDefer("TEST-1", "2027-01-01T00:00:00Z"))

	entries, err := app.store.GetActivity(context.Background(), "TEST-1", 100, 0)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	last := entries[len(entries)-1]
	assert.Equal(t, model.ActivityTypeStatusChange, last.Type)
	assert.Equal(t, "agent-7", last.Author, "activity must carry the process author")

	var author string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT author_id FROM sync_events WHERE node_id = ? AND op_type = ?`,
		"TEST-1", string(model.OpTransitionStatus)).Scan(&author))
	assert.Equal(t, "agent-7", author, "sync event must carry the process author")
}

// TestDefer_WithoutUntil_StoresNoWakeTime verifies a defer without --until
// stores no wake time, clearing an earlier one, and that its transition and
// sync event are the 0.5.x ones: exactly one sync event, a transition_status
// open→deferred with the CLI reason; a re-defer emits none (MTIX-95.22).
func TestDefer_WithoutUntil_StoresNoWakeTime(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"fresh open node", func(_ *testing.T) {}},
		{"stale wake time left by an earlier deferral", func(t *testing.T) {
			_, err := app.store.WriteDB().ExecContext(context.Background(),
				`UPDATE nodes SET defer_until = ? WHERE id = ?`,
				"2026-01-01T00:00:00Z", "TEST-1")
			require.NoError(t, err)
		}},
		{"re-defer of a node deferred with a wake time", func(t *testing.T) {
			require.NoError(t, runDefer("TEST-1", "2027-01-01T00:00:00Z"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Defer Plain", "", "", 3, "", "", "", "", ""))
			tt.setup(t)

			require.NoError(t, runDefer("TEST-1", ""))

			assert.False(t, rawDeferUntil(t, "TEST-1").Valid,
				"a defer without --until must leave no wake time")
			assert.Equal(t, []model.OpType{model.OpTransitionStatus}, nodeEventOps(t, "TEST-1"),
				"one transition_status event, as in 0.5.x; a re-defer adds none")

			var payload string
			require.NoError(t, app.store.QueryRow(context.Background(),
				`SELECT payload FROM sync_events WHERE node_id = ? AND op_type = ?`,
				"TEST-1", string(model.OpTransitionStatus)).Scan(&payload))
			var p model.TransitionStatusPayload
			require.NoError(t, json.Unmarshal([]byte(payload), &p))
			assert.Equal(t, model.StatusOpen, p.From)
			assert.Equal(t, model.StatusDeferred, p.To)
			assert.Equal(t, "deferred via CLI", p.Reason)
		})
	}
}

// TestDefer_UntilInPast_AutoWakesOnNextBackgroundPass verifies FR-3.8b end to
// end with an injected clock. The wake time has passed when defer_until <= now
// (one rule for ready, wake and claim, MTIX-95.22 round 2): a node deferred
// through the CLI with a wake time that has passed, or equals now, is listed
// by ready and reopened by the next background pass, which also clears the
// wake time; one with a future wake time stays deferred, out of ready, and
// cannot be claimed.
func TestDefer_UntilInPast_AutoWakesOnNextBackgroundPass(t *testing.T) {
	now := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	tests := []struct {
		name       string
		until      time.Time
		wantReady  bool // listed by ready before the background pass
		wantStatus model.Status
	}{
		{"wake time passed", now.Add(-time.Hour), true, model.StatusOpen},
		{"wake time equal to now", now, true, model.StatusOpen},
		{"wake time in the future", now.Add(time.Hour), false, model.StatusDeferred},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			app.store.SetClock(clock)
			bg := service.NewBackgroundService(app.store, nil, nil, clock)
			ctx := context.Background()
			require.NoError(t, runCreate("Defer Wake", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runDefer("TEST-1", tt.until.Format(time.RFC3339)))

			ready, err := bg.GetReadyNodes(ctx)
			require.NoError(t, err)
			inReady := false
			for _, n := range ready {
				inReady = inReady || n.ID == "TEST-1"
			}
			assert.Equal(t, tt.wantReady, inReady, "ready before the background pass")

			require.NoError(t, bg.RunScan(ctx))

			node, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, node.Status)
			if tt.wantStatus == model.StatusOpen {
				assert.Nil(t, node.DeferUntil, "the wake pass clears the wake time")
				return
			}
			err = app.store.ClaimNode(ctx, "TEST-1", "agent-1")
			assert.ErrorIs(t, err, model.ErrStillDeferred,
				"a claim before the wake time must be refused")
		})
	}
}

// TestRunDefer_Output_ReportsWakeTime verifies the defer output tells the
// user the stored wake time, in UTC, when there is one (MTIX-95.22).
func TestRunDefer_Output_ReportsWakeTime(t *testing.T) {
	tests := []struct {
		name    string
		json    bool
		until   string
		want    string
		notWant string
	}{
		{"json with until", true, "2027-01-01T01:00:00+01:00", `"defer_until": "2027-01-01T00:00:00Z"`, ""},
		{"json without until", true, "", `"status": "deferred"`, "defer_until"},
		{"human with until", false, "2027-01-01T01:00:00+01:00", "Deferred TEST-1 until 2027-01-01T00:00:00Z", ""},
		{"human without until", false, "", "Deferred TEST-1", "until"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Defer Output", "", "", 3, "", "", "", "", ""))
			app.jsonOutput = tt.json

			out := captureStdout(t, func() {
				require.NoError(t, runDefer("TEST-1", tt.until))
			})

			assert.Contains(t, out, tt.want)
			if tt.notWant != "" {
				assert.NotContains(t, out, tt.notWant)
			}
		})
	}
}

// TestDefer_UntilOutsideStorableYears_IsRejected verifies `mtix defer --until`
// rejects a valid RFC 3339 time whose UTC year falls outside 1..9999 (the
// stored text could not be read back, and would break show and list for the
// project), leaving the node deferrable and readable; the last and first
// storable seconds are accepted (MTIX-95.22 round 3).
func TestDefer_UntilOutsideStorableYears_IsRejected(t *testing.T) {
	tests := []struct {
		name  string
		until string
		want  string // stored text; "" means rejected
	}{
		{"one second past year 9999", "9999-12-31T19:00:00-05:00", ""},
		{"year 10000 in utc", "9999-12-31T23:00:00-05:00", ""},
		{"one second before year 1", "0001-01-01T00:59:59+01:00", ""},
		{"year -1 in utc", "0000-01-01T00:30:00+01:00", ""},
		{"last storable second", "9999-12-31T18:59:59-05:00", "9999-12-31T23:59:59Z"},
		{"first storable second", "0001-01-01T01:00:00+01:00", "0001-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Defer Edge", "", "", 3, "", "", "", "", ""))

			err := runDefer("TEST-1", tt.until)

			ctx := context.Background()
			node, getErr := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, getErr, "the node stays readable")
			_, _, listErr := app.store.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 10})
			require.NoError(t, listErr, "the project stays listable")
			if tt.want == "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid --until timestamp")
				assert.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Equal(t, model.StatusOpen, node.Status)
				assert.False(t, rawDeferUntil(t, "TEST-1").Valid)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, rawDeferUntil(t, "TEST-1").String)
		})
	}
}
