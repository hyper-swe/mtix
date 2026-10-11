// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// These tests verify retained-owner rearm against the durable inbox eligibility rules.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func wakeJournalStore(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	s, path := wakeStore(t)
	require.NoError(t, s.CreateNode(t.Context(), makeRootNode("TEST-1", "TEST", "Wake lease work", wakeTime())))
	return s, path
}

func wakeEvent(t *testing.T, s *sqlite.Store, path, id, to string) int64 {
	t.Helper()
	require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{ID: id, Author: "TEST-sender", Text: "Handle this event", CreatedAt: wakeTime(), Addressee: to}}))
	var seq int64
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT MAX(rowid) FROM sync_events").Scan(&seq))
	return seq
}

func requireWakeLease(t *testing.T, s *sqlite.Store) sqlite.WakeLease {
	t.Helper()
	lease, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.NoError(t, err)
	return lease
}

func TestReleaseWakeLease_EventArrivedMidRun_ReportsRelaunch(t *testing.T) {
	s, path := wakeJournalStore(t)
	lease := requireWakeLease(t, s)
	seq := wakeEvent(t, s, path, "TEST-ann1", wakeKey().AgentID)
	result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
	require.NoError(t, err)
	assert.Equal(t, "relaunch", result.Outcome)
	assert.Equal(t, 1, result.Pending)
	assert.GreaterOrEqual(t, result.HighWater, seq)
	require.NotNil(t, result.ExpiresAt)
	var holder string
	var dirty int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT holder,dirty FROM wake_leases").Scan(&holder, &dirty))
	assert.Equal(t, lease.Holder, holder)
	assert.Zero(t, dirty)
	require.NoError(t, s.InboxAck(t.Context(), wakeKey().AgentID, seq))
	result, err = s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
	require.NoError(t, err)
	assert.Equal(t, "released", result.Outcome)
	assert.Nil(t, result.ExpiresAt)
}

func TestReleaseWakeLease_DirtyButAllAcked_Releases(t *testing.T) {
	s, path := wakeJournalStore(t)
	seq := wakeEvent(t, s, path, "TEST-ann1", wakeKey().AgentID)
	lease := requireWakeLease(t, s)
	_, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	require.NoError(t, s.InboxAck(t.Context(), wakeKey().AgentID, seq))
	result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
	require.NoError(t, err)
	assert.Equal(t, "released", result.Outcome)
	assert.Zero(t, result.Pending)
}

func TestReleaseWakeLease_DeliveryBelowMarkWithDirty_ReportsRelaunch(t *testing.T) {
	s, path := wakeJournalStore(t)
	seq := wakeEvent(t, s, path, "TEST-ann1", "TEST-other")
	lease := requireWakeLease(t, s)
	require.LessOrEqual(t, seq, lease.HighWater)
	require.NoError(t, s.RecordInboxDelivery(t.Context(), wakeKey().AgentID, seq, "TEST-hook"))
	_, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", time.Minute)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
	require.NoError(t, err)
	assert.Equal(t, "relaunch", result.Outcome)
	assert.Equal(t, 1, result.Pending)
}

func TestReleaseWakeLease_CleanEventsBelowMark_Releases(t *testing.T) {
	s, path := wakeJournalStore(t)
	wakeEvent(t, s, path, "TEST-ann1", wakeKey().AgentID)
	lease := requireWakeLease(t, s)
	result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
	require.NoError(t, err)
	assert.Equal(t, "released", result.Outcome)
	assert.Equal(t, 1, result.Pending)
}

func TestReleaseWakeLease_StaleHolder_ReplacementUntouched(t *testing.T) {
	s, path := wakeStore(t)
	first := requireWakeLease(t, s)
	later := openWakeStore(t, path, wakeTime().Add(2*time.Minute))
	second := requireWakeLease(t, later)
	_, err := s.ReleaseWakeLease(t.Context(), wakeKey(), first.Holder, time.Minute, true)
	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	var holder string
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT holder FROM wake_leases").Scan(&holder))
	assert.Equal(t, second.Holder, holder)
}

func TestReleaseWakeLease_Abandon_DeletesAndCountsPending(t *testing.T) {
	s, path := wakeJournalStore(t)
	wakeEvent(t, s, path, "TEST-ann1", wakeKey().AgentID)
	lease := requireWakeLease(t, s)
	result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, true)
	require.NoError(t, err)
	assert.Equal(t, "abandoned", result.Outcome)
	assert.Equal(t, 1, result.Pending)
	var count int
	require.NoError(t, newTestDB(t, path).QueryRow("SELECT COUNT(*) FROM wake_leases").Scan(&count))
	assert.Zero(t, count)
	_, err = s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, false)
	assert.ErrorIs(t, err, model.ErrAlreadyClaimed)
}

func TestReleaseWakeLease_AcknowledgementModes_MatchInboxEligibility(t *testing.T) {
	for _, mode := range []string{"Selective", "Through"} {
		t.Run(mode, func(t *testing.T) {
			s, path := wakeJournalStore(t)
			first := wakeEvent(t, s, path, "TEST-ann1", wakeKey().AgentID)
			lease := requireWakeLease(t, s)
			last := wakeEvent(t, s, path, "TEST-ann2", wakeKey().AgentID)
			require.NoError(t, s.RecordInboxDelivery(t.Context(), wakeKey().AgentID, last, "TEST-hook"))
			if mode == "Selective" {
				require.NoError(t, s.InboxAck(t.Context(), wakeKey().AgentID, last))
			} else {
				require.NoError(t, s.InboxAckThrough(t.Context(), wakeKey().AgentID, first))
			}
			inbox, err := s.InboxList(t.Context(), wakeKey().AgentID)
			require.NoError(t, err)
			result, err := s.ReleaseWakeLease(t.Context(), wakeKey(), lease.Holder, time.Minute, true)
			require.NoError(t, err)
			assert.Equal(t, len(inbox), result.Pending)
			assert.Equal(t, 1, result.Pending)
		})
	}
}
