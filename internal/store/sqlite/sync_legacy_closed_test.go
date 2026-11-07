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
