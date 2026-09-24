// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-95.22: Store.DeferNode transitions a node to deferred and stores its
// wake time (defer_until) in the same transaction; the 0.5.x sync event stays
// a transition_status event.

// deferTestNow is the fixed store clock of the defer tests.
var deferTestNow = time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)

// newDeferTestStore opens a store with a fixed clock and one node, PROJ-1, in
// the given status.
func newDeferTestStore(t *testing.T, status model.Status) *sqlite.Store {
	t.Helper()
	s := newTestStore(t)
	s.SetClock(func() time.Time { return deferTestNow })
	node := makeRootNode("PROJ-1", "PROJ", "Defer Node", deferTestNow.Add(-time.Hour))
	node.Status = status
	require.NoError(t, s.CreateNode(context.Background(), node))
	return s
}

// deferColumn reads PROJ-1's defer_until column verbatim.
func deferColumn(t *testing.T, s *sqlite.Store) sql.NullString {
	t.Helper()
	var v sql.NullString
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT defer_until FROM nodes WHERE id = ?`, "PROJ-1").Scan(&v))
	return v
}

// deferEvents returns the op_type of every sync event of PROJ-1 after its
// create event, oldest first.
func deferEvents(t *testing.T, s *sqlite.Store) []model.OpType {
	t.Helper()
	rows, err := s.Query(context.Background(),
		`SELECT op_type FROM sync_events
		 WHERE node_id = ? AND op_type != ? ORDER BY lamport_clock`,
		"PROJ-1", string(model.OpCreateNode))
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

// timePtr returns a pointer to t.
func timePtr(t time.Time) *time.Time { return &t }

// TestDeferNode_FromOpen_StoresWakeTimeWithTransition verifies the defer
// stores defer_until in UTC with the transition and emits exactly one
// transition_status event open→deferred, whose payload is unchanged from
// 0.5.x (MTIX-95.22, FR-3.8b).
func TestDeferNode_FromOpen_StoresWakeTimeWithTransition(t *testing.T) {
	zone := time.FixedZone("UTC-3", -3*60*60)
	tests := []struct {
		name      string
		until     *time.Time
		wantValid bool
		want      string
	}{
		{"with until in utc", timePtr(time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)), true, "2031-01-01T00:00:00Z"},
		{"with until in another zone", timePtr(time.Date(2030, 12, 31, 21, 0, 0, 0, zone)), true, "2031-01-01T00:00:00Z"},
		{"without until", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()

			require.NoError(t, s.DeferNode(ctx, "PROJ-1", tt.until, "waiting on review", "agent-7"))

			got, err := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, err)
			assert.Equal(t, model.StatusDeferred, got.Status)
			col := deferColumn(t, s)
			assert.Equal(t, tt.wantValid, col.Valid)
			assert.Equal(t, tt.want, col.String)

			assert.Equal(t, []model.OpType{model.OpTransitionStatus}, deferEvents(t, s))
			var payload, author string
			require.NoError(t, s.QueryRow(ctx,
				`SELECT payload, author_id FROM sync_events WHERE node_id = ? AND op_type = ?`,
				"PROJ-1", string(model.OpTransitionStatus)).Scan(&payload, &author))
			var p model.TransitionStatusPayload
			require.NoError(t, json.Unmarshal([]byte(payload), &p))
			assert.Equal(t, model.TransitionStatusPayload{
				From: model.StatusOpen, To: model.StatusDeferred, Reason: "waiting on review",
			}, p)
			assert.Equal(t, "agent-7", author)
		})
	}
}

// TestDeferNode_WithoutUntil_ClearsEarlierWakeTime verifies a defer without
// until writes NULL over a wake time left by an earlier deferral
// (MTIX-95.22).
func TestDeferNode_WithoutUntil_ClearsEarlierWakeTime(t *testing.T) {
	s := newDeferTestStore(t, model.StatusOpen)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`, "2030-01-01T00:00:00Z", "PROJ-1")
	require.NoError(t, err)

	require.NoError(t, s.DeferNode(ctx, "PROJ-1", nil, "deferred", "agent-7"))

	assert.False(t, deferColumn(t, s).Valid)
}

// TestDeferNode_AlreadyDeferred_UpdatesWakeTimeWithoutEvent verifies a
// re-defer with a different wake time updates defer_until and updated_at and
// records a status_change activity entry with defer_until_changed {from, to}
// metadata, but emits no sync event; a re-defer with the same wake time
// changes nothing (FR-7.7a, MTIX-95.22).
func TestDeferNode_AlreadyDeferred_UpdatesWakeTimeWithoutEvent(t *testing.T) {
	first := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2031, 2, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		redefer      *time.Time
		wantColumn   sql.NullString
		wantActivity int // entries added by the re-defer
	}{
		{"new wake time", &second, sql.NullString{String: "2031-02-01T00:00:00Z", Valid: true}, 1},
		{"no wake time clears it", nil, sql.NullString{}, 1},
		{"same wake time is a no-op", &first, sql.NullString{String: "2031-01-01T00:00:00Z", Valid: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()
			require.NoError(t, s.DeferNode(ctx, "PROJ-1", &first, "deferred", "agent-7"))
			before, err := s.GetActivity(ctx, "PROJ-1", 100, 0)
			require.NoError(t, err)
			beforeNode, err := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, err)
			s.SetClock(func() time.Time { return deferTestNow.Add(time.Hour) })

			require.NoError(t, s.DeferNode(ctx, "PROJ-1", tt.redefer, "deferred again", "agent-8"))

			assert.Equal(t, tt.wantColumn, deferColumn(t, s))
			assert.Equal(t, []model.OpType{model.OpTransitionStatus}, deferEvents(t, s),
				"a re-defer emits no sync event")
			after, err := s.GetActivity(ctx, "PROJ-1", 100, 0)
			require.NoError(t, err)
			require.Len(t, after, len(before)+tt.wantActivity)
			got, err := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, err)
			assert.Equal(t, model.StatusDeferred, got.Status)
			if tt.wantActivity == 0 {
				assert.Equal(t, beforeNode.UpdatedAt, got.UpdatedAt)
				return
			}
			assert.Equal(t, deferTestNow.Add(time.Hour), got.UpdatedAt)
			entry := after[len(after)-1]
			assert.Equal(t, model.ActivityTypeStatusChange, entry.Type)
			assert.Equal(t, "agent-8", entry.Author)
			assert.Equal(t, "deferred again", entry.Text)
			var meta map[string]map[string]*string
			require.NoError(t, json.Unmarshal(entry.Metadata, &meta))
			change := meta["defer_until_changed"]
			require.NotNil(t, change, "FR-7.7a: metadata carries defer_until_changed")
			require.NotNil(t, change["from"])
			assert.Equal(t, "2031-01-01T00:00:00Z", *change["from"])
			if tt.wantColumn.Valid {
				require.NotNil(t, change["to"])
				assert.Equal(t, tt.wantColumn.String, *change["to"])
			} else {
				assert.Nil(t, change["to"], "no wake time is JSON null")
			}
		})
	}
}

// TestDeferNode_InvalidTransition_LeavesNodeUnchanged verifies a defer the
// state machine rejects stores no wake time: the transition and the wake time
// commit together or not at all (MTIX-95.22).
func TestDeferNode_InvalidTransition_LeavesNodeUnchanged(t *testing.T) {
	s := newDeferTestStore(t, model.StatusDone)
	ctx := context.Background()

	err := s.DeferNode(ctx, "PROJ-1", timePtr(time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)), "deferred", "agent-7")
	require.ErrorIs(t, err, model.ErrInvalidTransition)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, got.Status)
	assert.False(t, deferColumn(t, s).Valid)
	assert.Empty(t, deferEvents(t, s))
}

// TestDeferNode_MissingNode_ReturnsNotFound verifies a defer of a node that
// does not exist, or is soft-deleted, returns ErrNotFound (MTIX-95.22).
func TestDeferNode_MissingNode_ReturnsNotFound(t *testing.T) {
	s := newDeferTestStore(t, model.StatusOpen)
	ctx := context.Background()
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	for _, id := range []string{"PROJ-99", "PROJ-1"} {
		err := s.DeferNode(ctx, id, nil, "deferred", "agent-7")
		assert.ErrorIs(t, err, model.ErrNotFound, id)
	}
}

// TestDeferNode_LocalExitFromDeferred_ClearsWakeTime verifies every local way
// out of deferred clears defer_until in the same transaction as the status
// change: the wake pass and reopen (TransitionStatus to open), claim, cancel,
// invalidation, and a cascade cancel that reaches a deferred child
// (MTIX-95.22 round 2). A wake time kept after the node left deferred would
// wake a later deferral that has none, such as one received through sync.
func TestDeferNode_LocalExitFromDeferred_ClearsWakeTime(t *testing.T) {
	until := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		exit       func(t *testing.T, s *sqlite.Store) string // returns the id to check
		wantStatus model.Status
	}{
		{"wake or reopen", func(t *testing.T, s *sqlite.Store) string {
			require.NoError(t, s.TransitionStatus(context.Background(), "PROJ-1", model.StatusOpen,
				"Auto-reopened: defer_until has passed", "system"))
			return "PROJ-1"
		}, model.StatusOpen},
		{"claim after the wake time", func(t *testing.T, s *sqlite.Store) string {
			s.SetClock(func() time.Time { return until.Add(time.Second) })
			require.NoError(t, s.ClaimNode(context.Background(), "PROJ-1", "agent-1"))
			return "PROJ-1"
		}, model.StatusInProgress},
		{"cancel", func(t *testing.T, s *sqlite.Store) string {
			require.NoError(t, s.CancelNode(context.Background(), "PROJ-1", "dropped", "agent-1", false))
			return "PROJ-1"
		}, model.StatusCancelled},
		{"invalidate", func(t *testing.T, s *sqlite.Store) string {
			require.NoError(t, s.TransitionStatus(context.Background(), "PROJ-1", model.StatusInvalidated,
				"parent prompt changed", "system"))
			return "PROJ-1"
		}, model.StatusInvalidated},
		{"cascade cancel of a deferred child", func(t *testing.T, s *sqlite.Store) string {
			ctx := context.Background()
			child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, deferTestNow.Add(-time.Hour))
			require.NoError(t, s.CreateNode(ctx, child))
			require.NoError(t, s.DeferNode(ctx, "PROJ-1.1", &until, "deferred", "agent-1"))
			require.NoError(t, s.CancelNode(ctx, "PROJ-1", "dropped", "agent-1", true))
			return "PROJ-1.1"
		}, model.StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()
			require.NoError(t, s.DeferNode(ctx, "PROJ-1", &until, "deferred", "agent-1"))

			id := tt.exit(t, s)

			got, err := s.GetNode(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Nil(t, got.DeferUntil, "leaving deferred clears the wake time")
		})
	}
}

// TestDeferNode_WakeTimeWriteFails_RollsBackTheWholeDefer verifies the defer
// is one transaction: when the defer_until write fails after the status
// write, the status, the activity entry and the sync event all roll back
// (MTIX-95.22 round 2).
func TestDeferNode_WakeTimeWriteFails_RollsBackTheWholeDefer(t *testing.T) {
	s := newDeferTestStore(t, model.StatusOpen)
	ctx := context.Background()
	before, err := s.GetActivity(ctx, "PROJ-1", 100, 0)
	require.NoError(t, err)
	// Test seam: abort any write that sets defer_until, which the defer makes
	// after its status write, activity entry and sync event.
	_, err = s.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER abort_defer_until BEFORE UPDATE OF defer_until ON nodes
		BEGIN SELECT RAISE(ABORT, 'defer_until write refused by test'); END`)
	require.NoError(t, err)

	err = s.DeferNode(ctx, "PROJ-1", timePtr(time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)), "deferred", "agent-7")
	require.Error(t, err)
	require.Contains(t, err.Error(), "defer_until write refused by test")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status, "the status write rolled back")
	after, err := s.GetActivity(ctx, "PROJ-1", 100, 0)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the activity entry rolled back")
	assert.Empty(t, deferEvents(t, s), "the sync event rolled back")
	assert.False(t, deferColumn(t, s).Valid)
}

// TestClaimNode_AtWakeTime_Succeeds verifies the one boundary rule for a
// wake time: it has passed when defer_until <= now, so a claim at exactly the
// wake time succeeds and a claim one second before it is refused
// (MTIX-95.22 round 2).
func TestClaimNode_AtWakeTime_Succeeds(t *testing.T) {
	tests := []struct {
		name    string
		until   time.Time
		wantErr error
	}{
		{"one second before the wake time", deferTestNow.Add(time.Second), model.ErrStillDeferred},
		{"at the wake time", deferTestNow, nil},
		{"after the wake time", deferTestNow.Add(-time.Second), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()
			require.NoError(t, s.DeferNode(ctx, "PROJ-1", &tt.until, "deferred", "agent-1"))

			err := s.ClaimNode(ctx, "PROJ-1", "agent-1")

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestDeferNode_UntilOutsideStorableYears_ReturnsInvalidInput verifies the
// store refuses a wake time whose UTC year is outside 1..9999 whatever path
// reached it, because its RFC 3339 text could not be read back and would
// break every read of the node and of its project (MTIX-95.22 round 3).
func TestDeferNode_UntilOutsideStorableYears_ReturnsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		until time.Time
	}{
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"year 9999 west of utc crossing into 10000", time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60))},
		{"year 0", time.Date(0, 6, 1, 0, 0, 0, 0, time.UTC)},
		{"year -1", time.Date(-1, 6, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()

			err := s.DeferNode(ctx, "PROJ-1", &tt.until, "deferred", "agent-7")
			require.ErrorIs(t, err, model.ErrInvalidInput)

			got, err := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, err, "the node stays readable")
			assert.Equal(t, model.StatusOpen, got.Status)
			assert.False(t, deferColumn(t, s).Valid)
			assert.Empty(t, deferEvents(t, s))
		})
	}
}

// TestCreateNode_DeferUntilOutsideStorableYears_ReturnsInvalidInput verifies
// the create path refuses an unreadable wake time too (MTIX-95.22 round 3).
func TestCreateNode_DeferUntilOutsideStorableYears_ReturnsInvalidInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	node := makeRootNode("PROJ-1", "PROJ", "Create", deferTestNow)
	until := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	node.DeferUntil = &until

	require.ErrorIs(t, s.CreateNode(ctx, node), model.ErrInvalidInput)
	_, err := s.GetNode(ctx, "PROJ-1")
	require.ErrorIs(t, err, model.ErrNotFound)
}
