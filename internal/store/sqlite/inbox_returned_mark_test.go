// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-112.18.1 (A12): the returned-seq mark records the highest event seq
// already returned to an agent id, so a budget charges only new events.
func TestInboxReturnedMark_RegisteredAndUnregistered_AdvanceMonotonic(t *testing.T) {
	s, path := newSignalsStore(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	db := newTestDB(t, path)
	_, err := db.Exec("INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark) VALUES('reg','local','u1','hostA','manual','t0','t0','0.6.0',1,0)")
	require.NoError(t, err)

	tests := []struct {
		name  string
		agent string
	}{
		{"registered id uses the registration row", "reg"},
		{"unregistered id uses a per-id meta key", "loose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mark, err := s.InboxReturnedMark(t.Context(), tt.agent)
			require.NoError(t, err)
			require.Zero(t, mark)
			require.NoError(t, s.AdvanceInboxReturnedMark(t.Context(), tt.agent, 7))
			require.NoError(t, s.AdvanceInboxReturnedMark(t.Context(), tt.agent, 3), "a lower seq never rewinds the mark")
			mark, err = s.InboxReturnedMark(t.Context(), tt.agent)
			require.NoError(t, err)
			require.Equal(t, int64(7), mark)
		})
	}
	var stored int64
	require.NoError(t, db.QueryRow("SELECT returned_seq_mark FROM agent_registrations WHERE agent_id='reg'").Scan(&stored))
	require.Equal(t, int64(7), stored)
}

func TestInboxReturnedMark_InvalidInput_Refused(t *testing.T) {
	s, _ := newSignalsStore(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err := s.InboxReturnedMark(t.Context(), "")
	require.Error(t, err)
	require.Error(t, s.AdvanceInboxReturnedMark(t.Context(), "", 1))
	require.Error(t, s.AdvanceInboxReturnedMark(t.Context(), "a", -1))
}

func TestInboxReturnedMark_CorruptMetaAndClosedStore_Error(t *testing.T) {
	s, path := newSignalsStore(t)
	db := newTestDB(t, path)
	_, err := db.Exec("INSERT INTO meta(key,value) VALUES('inbox.returned_seq.bad','not-a-number')")
	require.NoError(t, err)
	_, err = s.InboxReturnedMark(t.Context(), "bad")
	require.Error(t, err, "a corrupt mark is an error, not zero")
	require.NoError(t, s.Close())
	_, err = s.InboxReturnedMark(t.Context(), "any")
	require.Error(t, err)
	require.Error(t, s.AdvanceInboxReturnedMark(t.Context(), "any", 1))
}

func TestInboxReturnedMark_SurvivesUnregisterAndReRegister(t *testing.T) {
	s, path := newSignalsStore(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	db := newTestDB(t, path)
	insert := "INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark) VALUES('reg','local','u1','hostA','manual','t0','t0','0.6.0',1,0)"
	_, err := db.Exec(insert)
	require.NoError(t, err)
	require.NoError(t, s.AdvanceInboxReturnedMark(t.Context(), "reg", 9))
	_, err = db.Exec("DELETE FROM agent_registrations WHERE agent_id='reg'")
	require.NoError(t, err)
	_, err = db.Exec(insert)
	require.NoError(t, err)
	mark, err := s.InboxReturnedMark(t.Context(), "reg")
	require.NoError(t, err)
	require.Equal(t, int64(9), mark, "a fresh registration row must not reset the mark")
}

func TestWakeChargedAfter_ReportsLedgerRowsAboveTheMark(t *testing.T) {
	s, path := newSignalsStore(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	db := newTestDB(t, path)
	for _, seq := range []int{3, 5, 8} {
		_, err := db.Exec("INSERT INTO wake_ledger(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome) VALUES('w','h','u',?,1,'2026-01-01T00:00:00Z','sent')", seq)
		require.NoError(t, err)
	}
	_, err := db.Exec("INSERT INTO wake_ledger(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome) VALUES('other','h','u',9,1,'2026-01-01T00:00:00Z','sent')")
	require.NoError(t, err)
	got, err := s.WakeChargedAfter(t.Context(), "w", 3)
	require.NoError(t, err)
	require.Equal(t, map[int64]bool{5: true, 8: true}, got)
	_, err = s.WakeChargedAfter(t.Context(), "", 0)
	require.Error(t, err)
}
