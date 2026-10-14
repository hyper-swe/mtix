// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests replay of accepted occurrences after interrupted native writes.
package oplocal

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAlertRaise_ProjectionBoundaries_ReplaysWithoutExtraCount(t *testing.T) {
	for _, stage := range []string{"before:alerts-pending.json", "after:alerts-pending.json", "before:alerts.2.jsonl", "after:alerts.2.jsonl", "before:alerts.1.jsonl", "after:alerts.1.jsonl", "before:alerts.jsonl", "after:alerts.jsonl", "before:rate", "after:rate", "before:clear", "after:clear"} {
		t.Run(stage, func(t *testing.T) {
			fixture := projectionBoundaryFixture(t)
			s := fixture.state
			injected := errors.New("injected projection stop")
			hit := false
			s.alertStage = func(got string) error {
				if got == stage && !hit {
					hit = true
					return injected
				}
				return nil
			}
			input := alertInput()
			input.Agent = "pending-agent"
			_, err := s.RecordAlert(t.Context(), input, alertTime())
			require.ErrorIs(t, err, injected)
			require.True(t, hit)
			restarted := New(s.env)
			assertProjectionPendingRows(t, restarted, input, stage)
			next, err := restarted.RecordAlert(t.Context(), input, alertTime())
			require.NoError(t, err)
			want := uint64(2)
			if stage == "before:alerts-pending.json" {
				want = 1
			}
			require.Equal(t, want, next.Count)
			if stage != "before:alerts-pending.json" {
				dir, openErr := restarted.directory(false)
				require.NoError(t, openErr)
				seg, readErr := readAlertSegments(dir, fixture.first.HostID)
				require.NoError(t, readErr)
				require.NoError(t, closeFile(dir))
				require.Equal(t, fixture.active, seg[1].data, fmt.Sprint(stage))
				require.Equal(t, fixture.archive, seg[2].data)
			}
		})
	}
}

type alertProjectionFixture struct {
	state   *State
	first   Alert
	active  []byte
	archive []byte
}

func projectionBoundaryFixture(t *testing.T) alertProjectionFixture {
	t.Helper()
	s := alertFixture(t)
	first, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	active := fullAlertSegment(t, first.HostID, nil)
	seedAlertSegment(t, s, 0, active)
	old := first
	old.Agent = "old"
	old.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldData, err := encodeAlertSegment([]Alert{old})
	require.NoError(t, err)
	seedAlertSegment(t, s, 1, oldData)
	return alertProjectionFixture{state: s, first: first, active: active, archive: oldData}
}

func assertProjectionPendingRows(t *testing.T, restarted *State, input Alert, stage string) {
	t.Helper()
	rows, err := restarted.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	found := 0
	for _, row := range rows {
		if row.Agent == input.Agent {
			found++
			require.Equal(t, uint64(1), row.Count)
		}
	}
	if stage == "before:alerts-pending.json" {
		require.Zero(t, found)
	} else {
		require.Equal(t, 1, found)
	}
}
