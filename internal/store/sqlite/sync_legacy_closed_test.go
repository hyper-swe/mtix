// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestLegacyClosedStamps_NamesOnlyNodesStampedFromAPlainOldTerminalEvent pins
// the evidence the enrolment comparison uses (MTIX-112.17.16.19.1): a terminal
// event with no closed_at stamps the node from its wall clock and is named; an
// event that carries closed_at, even one equal to the wall clock, is not.
func TestLegacyClosedStamps_NamesOnlyNodesStampedFromAPlainOldTerminalEvent(t *testing.T) {
	wall := time.Date(2026, 3, 4, 5, 6, 7, 900_000_000, time.UTC)
	tests := []struct {
		name     string
		snapshot json.RawMessage
		wantName bool
	}{
		{"plain old done", nil, true},
		{"done carrying closed_at", json.RawMessage(`"2026-03-04T05:06:07Z"`), false},
		{"done carrying a null closed_at", json.RawMessage(`null`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := replicaWithNode(t)
			p := transition(model.StatusInProgress, model.StatusDone)
			p.ClosedAt = tt.snapshot
			done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, p, 2, "")
			done.WallClockTS = wall.UnixMilli()
			pullEvents(t, s, []*model.SyncEvent{done})

			got, err := s.LegacyClosedStamps(context.Background())

			require.NoError(t, err)
			if tt.wantName {
				require.Equal(t, map[string]string{"MTIX-1": "2026-03-04T05:06:07Z"}, got)
				return
			}
			require.Empty(t, got)
		})
	}
}

// A node that is not terminal, or whose stamp is not the event's second, is
// never named.
func TestLegacyClosedStamps_NonTerminalAndEditedStamp_AreNotNamed(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	start := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusOpen, model.StatusInProgress), 2, "")
	pullEvents(t, s, []*model.SyncEvent{start})
	got, err := s.LegacyClosedStamps(ctx)
	require.NoError(t, err)
	require.Empty(t, got)

	done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusInProgress, model.StatusDone), 3, "")
	pullEvents(t, s, []*model.SyncEvent{done})
	_, err = raw.ExecContext(ctx, `UPDATE nodes SET closed_at = '2001-01-01T00:00:00Z' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)
	got, err = s.LegacyClosedStamps(ctx)
	require.NoError(t, err)
	require.Empty(t, got, "a stamp that is not the event's wall-clock second is compared exactly")
}

// A stamp that is not on a terminal node, and a deleted node, are never named,
// even when a plain event's wall-clock second equals the stored value.
func TestLegacyClosedStamps_PlainNonTerminalAndDeletedNode_AreNotNamed(t *testing.T) {
	ctx := context.Background()
	wall := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	s, raw := replicaWithNode(t)
	plain := func(to model.Status, lamport int64) {
		e := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusOpen, to), lamport, "")
		e.WallClockTS = wall.UnixMilli()
		pullEvents(t, s, []*model.SyncEvent{e})
	}
	plain(model.StatusBlocked, 2)
	_, err := raw.ExecContext(ctx, `UPDATE nodes SET closed_at = '2026-03-04T05:06:07Z' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)
	got, err := s.LegacyClosedStamps(ctx)
	require.NoError(t, err)
	require.Empty(t, got, "a plain transition to a non-terminal status names nothing")

	plain(model.StatusDone, 3)
	got, err = s.LegacyClosedStamps(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"MTIX-1": "2026-03-04T05:06:07Z"}, got)
	_, err = raw.ExecContext(ctx, `UPDATE nodes SET deleted_at = '2026-03-05T00:00:00Z' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)
	got, err = s.LegacyClosedStamps(ctx)
	require.NoError(t, err)
	require.Empty(t, got, "a deleted node is not compared")
}

// The event that set closed_at is the node's winning workflow event. A plain
// terminal event that lost to a later event, modern or plain, names nothing, and
// neither does a plain terminal event under a later modern winner that carries
// the same second.
func TestLegacyClosedStamps_OnlyTheWinningWorkflowEventCounts(t *testing.T) {
	ctx := context.Background()
	wall := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	const stamp = "2026-03-04T05:06:07Z"
	tests := []struct {
		name  string
		later func(t *testing.T) *model.SyncEvent
		want  map[string]string
	}{
		{"a later modern done carrying the same second", func(t *testing.T) *model.SyncEvent {
			p := transition(model.StatusOpen, model.StatusDone)
			p.ClosedAt = json.RawMessage(`"` + stamp + `"`)
			return foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, p, 3, "")
		}, nil},
		{"a later modern invalidate carrying the same second", func(t *testing.T) *model.SyncEvent {
			p := transition(model.StatusDone, model.StatusInvalidated)
			p.ClosedAt = json.RawMessage(`"` + stamp + `"`)
			return foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, p, 3, "")
		}, nil},
		{"a later reopen", func(t *testing.T) *model.SyncEvent {
			return foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusDone, model.StatusOpen), 3, "")
		}, nil},
		{"a later plain done of the same second", func(t *testing.T) *model.SyncEvent {
			return foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusOpen, model.StatusDone), 3, "")
		}, map[string]string{"MTIX-1": stamp}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithNode(t)
			first := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusInProgress, model.StatusDone), 2, "")
			first.WallClockTS = wall.UnixMilli()
			later := tt.later(t)
			later.WallClockTS = wall.UnixMilli()
			pullEvents(t, s, []*model.SyncEvent{first, later})
			_, err := raw.ExecContext(ctx, `UPDATE nodes SET closed_at = ? WHERE id = 'MTIX-1'`, stamp)
			require.NoError(t, err)

			got, err := s.LegacyClosedStamps(ctx)

			require.NoError(t, err)
			if tt.want == nil {
				require.Empty(t, got)
				return
			}
			require.Equal(t, tt.want, got)
		})
	}
}

// The winner is scoped as apply scopes it: by the node's uid when its events
// carry one, by node id only when none does. Here the uid-scoped winner is a
// plain done of the stamp's second, while a later uid-less reopen is the
// highest event under the node id; the node is named from the uid walk.
func TestLegacyClosedStamps_UIDScopedWinner_BeatsNodeIDScopedWinner(t *testing.T) {
	ctx := context.Background()
	wall := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	const stamp = "2026-03-04T05:06:07Z"
	s, raw := replicaWithNode(t)
	uid := nodeRow(t, raw, "MTIX-1")["uid"]
	require.NotEqual(t, nullColumn, uid)
	done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusInProgress, model.StatusDone), 2, "")
	done.UID = uid
	done.WallClockTS = wall.UnixMilli()
	reopen := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus, transition(model.StatusDone, model.StatusOpen), 3, "")
	reopen.WallClockTS = wall.UnixMilli()
	pullEvents(t, s, []*model.SyncEvent{done, reopen})
	_, err := raw.ExecContext(ctx, `UPDATE nodes SET closed_at = ? WHERE id = 'MTIX-1'`, stamp)
	require.NoError(t, err)

	got, err := s.LegacyClosedStamps(ctx)

	require.NoError(t, err)
	require.Equal(t, map[string]string{"MTIX-1": stamp}, got,
		"the uid-carrying done is the winner; the uid-less reopen is outside the uid walk")
}
