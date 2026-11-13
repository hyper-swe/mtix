// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that stalled-turn recovery (MTIX-112.40.12, FR-20) is not starved by rows it must
// refuse, and that it reads nothing when no push route can redeliver.
package service_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ghostCount is more than one candidate window (16), so refused rows alone fill a pass.
const ghostCount = 20

// seedGhostRows writes sent, unacknowledged ledger rows for other agents below the real
// stalled event (same event_seq, agent id sorts before it), so they head the oldest-first order.
func seedGhostRows(t *testing.T, st *sqlite.Store, now time.Time, count int, register func(agent string)) {
	t.Helper()
	for i := 0; i < count; i++ {
		agent := fmt.Sprintf("ghost-%02d", i)
		_, err := st.WriteDB().ExecContext(t.Context(), `INSERT INTO wake_ledger
			(agent_id,host_id,uid,event_seq,epoch,claimed_at,outcome,attempt_token,outcome_at,reason)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			agent, bothRoutesRecipient.HostID, bothRoutesRecipient.UID, 1, 1,
			now.Format(time.RFC3339Nano), model.WakeSent, "tok-"+agent, now.Format(time.RFC3339Nano), "")
		require.NoError(t, err)
		if register != nil {
			register(agent)
		}
	}
}

func TestStalledRecovery_RefusedRowsAheadOfIt_DoNotStarveTheRealStall(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, st *sqlite.Store, now time.Time) // makes 20 rows the gates refuse
	}{
		{"unregistered or non-hook agents", func(t *testing.T, st *sqlite.Store, now time.Time) {
			seedGhostRows(t, st, now, ghostCount, nil)
		}},
		{"unregistered agents beyond the page cap", func(t *testing.T, st *sqlite.Store, now time.Time) {
			seedGhostRows(t, st, now, 200, nil) // more than 8 pages of 16: only the harness filter keeps the window clear
		}},
		{"hook agents with a live waiter", func(t *testing.T, st *sqlite.Store, now time.Time) {
			seedGhostRows(t, st, now, ghostCount, func(agent string) {
				_, err := st.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
					(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
					VALUES (?,?,?,?,'manual',1,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
					agent, procinfo.HarnessClaude, bothRoutesRecipient.UID, bothRoutesRecipient.HostID)
				require.NoError(t, err)
				_, err = st.WriteDB().ExecContext(t.Context(), `INSERT INTO wake_leases
					(agent_id,host_id,uid,holder,high_water,acquired_at,expires_at,dirty) VALUES (?,?,?,?,0,?,?,0)`,
					agent, bothRoutesRecipient.HostID, bothRoutesRecipient.UID, "h", now.Format(time.RFC3339Nano),
					now.Add(24*time.Hour).Format(time.RFC3339Nano))
				require.NoError(t, err)
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, now, alerts := wakeServiceStore(t)
			tally := &wakeTally{wakes: map[int64]int{}}
			d := markedStallDispatcher(t, st, alerts, tally, nil)
			sentBeforeMark(t, st, d, tally)
			tt.setup(t, st, *now)
			*now = now.Add(2 * stallWindow)
			require.NoError(t, d.Pass(t.Context(), nil))
			require.Equal(t, 2, tally.wakes[1], "the real stalled event is redelivered despite 20 refused rows ahead of it")
		})
	}
}

// registerLiveWaiter makes agent a hook-route registration holding an unexpired wake lease,
// so the waiter-live gate refuses its stalled row.
func registerLiveWaiter(t *testing.T, st *sqlite.Store, now time.Time) func(agent string) {
	return func(agent string) {
		_, err := st.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
			(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
			VALUES (?,?,?,?,'manual',1,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
			agent, procinfo.HarnessClaude, bothRoutesRecipient.UID, bothRoutesRecipient.HostID)
		require.NoError(t, err)
		_, err = st.WriteDB().ExecContext(t.Context(), `INSERT INTO wake_leases
			(agent_id,host_id,uid,holder,high_water,acquired_at,expires_at,dirty) VALUES (?,?,?,?,0,?,?,0)`,
			agent, bothRoutesRecipient.HostID, bothRoutesRecipient.UID, "h", now.Format(time.RFC3339Nano),
			now.Add(24*time.Hour).Format(time.RFC3339Nano))
		require.NoError(t, err)
	}
}

// Documented limit: a pass examines at most 128 candidates (8 pages of 16), so more than 128
// refused hook-route rows ahead of a real stall delay it past one pass.
func TestStalledRecovery_MoreThan128RefusedRows_DocumentedCapDelaysRecoveryPastOnePass(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, nil)
	sentBeforeMark(t, st, d, tally)
	seedGhostRows(t, st, *now, 129, registerLiveWaiter(t, st, *now))
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 1, tally.wakes[1], "129 refused rows fill the 128-candidate pass: the real stall waits")
}

func TestStalledRecovery_Exactly127RefusedRows_RecoversWithinOnePass(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, nil)
	sentBeforeMark(t, st, d, tally)
	seedGhostRows(t, st, *now, 127, registerLiveWaiter(t, st, *now))
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 2, tally.wakes[1], "127 refused rows plus the real one fit the cap")
}
