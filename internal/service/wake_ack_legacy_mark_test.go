// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

// legacyAckMark is a stored mark written by a build that counted raw journal
// rows. It is higher than the recipient-event mark the store reports now.
const legacyAckMark = 10

// TestWakeAckPublication_LegacyStoredMark_NewAckStillResetsOnce checks that a
// mark stored under the old row-counting semantics never rejects a newly
// effective acknowledgement and is replaced by the recipient-event mark.
func TestWakeAckPublication_LegacyStoredMark_NewAckStillResetsOnce(t *testing.T) {
	state, key, source := wakePublisherFixture(t)
	require.NoError(t, state.UpdateWakeBudget(t.Context(), key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		row := m.Sources[source]
		row.AckMark = legacyAckMark
		m.Sources[source] = row
		return save()
	}))
	publisher := NewWakeAckPublisher(wakePublisherBackend{source: source, mark: 2}, state, key.UID)
	require.NoError(t, publisher.Observe(t.Context(), key.AgentID, 1, 2))
	mirror, _, err := state.ReadWakeBudget(key)
	require.NoError(t, err)
	require.Zero(t, mirror.Unacked)
	require.Equal(t, int64(2), mirror.Sources[source].AckMark)
	require.Equal(t, int64(5), mirror.Credits[0].Count)
}
