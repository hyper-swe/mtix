// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInboxEventAcked_ReportsOnlyAcknowledgedEventsOfThatAgent pins the delivery evidence a
// reclaimed wake relies on (MTIX-112.40.13): an event counts as seen only after its
// recipient acknowledged it, selectively or through the cumulative cursor.
func TestInboxEventAcked_ReportsOnlyAcknowledgedEventsOfThatAgent(t *testing.T) {
	s, _ := budgetDataStore(t)
	first, second, third := addressThree(t, s, "worker")
	acked := func(agent string, seq int64) bool {
		got, err := s.InboxEventAcked(t.Context(), agent, seq)
		require.NoError(t, err)
		return got
	}
	require.False(t, acked("worker", first), "an unread event is not evidence")
	require.NoError(t, s.InboxAck(t.Context(), "worker", second))
	require.True(t, acked("worker", second), "a selective acknowledgement is evidence")
	require.False(t, acked("worker", first), "a selective ack covers only its own event")
	require.False(t, acked("worker", third))
	require.False(t, acked("other-agent", second), "another agent's acknowledgement is not evidence")
	require.NoError(t, s.InboxAckThrough(t.Context(), "worker", third))
	require.True(t, acked("worker", first), "the cumulative cursor covers every earlier event")
	require.True(t, acked("worker", third))
	require.False(t, acked("worker", third+100), "an event above the cursor is not covered")
}

func TestInboxEventAcked_InvalidInput_IsRefused(t *testing.T) {
	s, _ := budgetDataStore(t)
	_, err := s.InboxEventAcked(t.Context(), "", 1)
	require.Error(t, err)
	_, err = s.InboxEventAcked(t.Context(), "worker", 0)
	require.Error(t, err)
}
