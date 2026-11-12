// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the store half of stalled-turn recovery (MTIX-112.40.12, FR-20): the one
// compare-and-set reopen of a sent wake whose confirmation never came, its retry cap of
// one, and the live-waiter read the stall rule needs.
package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func stallKey(seq int64) model.WakeKey {
	return model.WakeKey{AgentID: "TEST-agent", HostID: "TEST-host", UID: "1000", EventSeq: seq, Epoch: 1}
}

// sentRow claims an event and records it sent at the store's current clock.
func sentRow(t *testing.T, s *sqlite.Store, key model.WakeKey) {
	t.Helper()
	attempt, won, err := s.ClaimWake(context.Background(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeResult(context.Background(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}))
}

// reopenStalled reopens under an admission that allows every claim, so the tests judge the
// stall rule and the cap alone; the budget gate has its own tests.
func reopenStalled(s *sqlite.Store, key model.WakeKey, window time.Duration) (model.WakeAttempt, bool, error) {
	res, err := s.ReopenStalledWake(context.Background(), key, window, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	})
	return res.Attempt, res.Won, err
}

func TestReopenStalledWake_SentPastWindow_ReopensOnceThenCaps(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/a.db", now)
	key := stallKey(1)
	sentRow(t, s, key)
	later := now.Add(time.Minute)
	s.SetClock(func() time.Time { return later })

	attempt, ok, err := reopenStalled(s, key, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok, "a sent row older than the window is reopened")
	require.True(t, attempt.Reclaimed)
	require.NotEmpty(t, attempt.Token)
	got, found, err := s.ReadWakeResult(context.Background(), key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, model.WakeClaimed, got.Outcome)

	// The reopened claim is finished (sent again); a second stall hits the cap of one.
	require.NoError(t, s.RecordWakeResult(context.Background(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "claude-socket"}))
	later2 := later.Add(time.Minute)
	s.SetClock(func() time.Time { return later2 })
	_, ok, err = reopenStalled(s, key, 30*time.Second)
	require.NoError(t, err)
	require.False(t, ok, "the retry cap is one")
	got, _, err = s.ReadWakeResult(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeUndeliverable, got.Outcome)
	require.Equal(t, model.WakeReasonStallUnconfirmed, got.Reason)
}

func TestReopenStalledWake_NotStalledRows_AreUntouched(t *testing.T) {
	now := wakeTime()
	window := 30 * time.Second
	tests := []struct {
		name  string
		setup func(t *testing.T, s *sqlite.Store, key model.WakeKey)
		want  model.WakeOutcome
	}{
		{"sent within the window", func(t *testing.T, s *sqlite.Store, key model.WakeKey) { sentRow(t, s, key) }, model.WakeSent},
		{"confirmed", func(t *testing.T, s *sqlite.Store, key model.WakeKey) {
			sentRow(t, s, key)
			require.NoError(t, s.UpgradeWakeConfirmed(context.Background(), key))
		}, model.WakeConfirmed},
		{"confirmed and long past the window", func(t *testing.T, s *sqlite.Store, key model.WakeKey) {
			sentRow(t, s, key)
			require.NoError(t, s.UpgradeWakeConfirmed(context.Background(), key))
			s.SetClock(func() time.Time { return now.Add(10 * time.Minute) })
		}, model.WakeConfirmed},
		{"in-flight claim", func(t *testing.T, s *sqlite.Store, key model.WakeKey) {
			_, won, err := s.ClaimWake(context.Background(), key, time.Minute)
			require.NoError(t, err)
			require.True(t, won)
		}, model.WakeClaimed},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openWakeStore(t, t.TempDir()+"/b.db", now)
			key := stallKey(int64(i + 1))
			tt.setup(t, s, key)
			_, ok, err := reopenStalled(s, key, window)
			require.NoError(t, err)
			require.False(t, ok)
			got, _, err := s.ReadWakeResult(context.Background(), key)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.Outcome)
		})
	}
	t.Run("no row", func(t *testing.T) {
		s := openWakeStore(t, t.TempDir()+"/c.db", now)
		_, ok, err := reopenStalled(s, stallKey(9), window)
		require.NoError(t, err)
		require.False(t, ok)
	})
	t.Run("non-positive window", func(t *testing.T) {
		s := openWakeStore(t, t.TempDir()+"/g.db", now)
		_, _, err := reopenStalled(s, stallKey(1), 0)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	})
	t.Run("invalid key", func(t *testing.T) {
		s := openWakeStore(t, t.TempDir()+"/d.db", now)
		_, _, err := reopenStalled(s, model.WakeKey{}, window)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	})
}

func TestReopenStalledWake_ConcurrentReopeners_ExactlyOneWins(t *testing.T) {
	now := wakeTime()
	dir := t.TempDir()
	first := openWakeStore(t, dir+"/e.db", now)
	key := stallKey(1)
	sentRow(t, first, key)
	later := now.Add(time.Minute)
	first.SetClock(func() time.Time { return later })
	second := openWakeStore(t, dir+"/e.db", later)
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(s *sqlite.Store) {
			defer wg.Done()
			_, ok, err := reopenStalled(s, key, 30*time.Second)
			require.NoError(t, err)
			wins <- ok
		}([]*sqlite.Store{first, second}[i%2])
	}
	wg.Wait()
	close(wins)
	won := 0
	for ok := range wins {
		if ok {
			won++
		}
	}
	require.Equal(t, 1, won)
}

func TestWakeWaiterLive_FollowsLeaseExpiry(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/f.db", now)
	live, err := s.WakeWaiterLive(context.Background(), wakeKey())
	require.NoError(t, err)
	require.False(t, live, "no lease row: nobody is waiting")
	_, err = s.AcquireWakeLease(context.Background(), wakeKey(), "", 15*time.Minute)
	require.NoError(t, err)
	live, err = s.WakeWaiterLive(context.Background(), wakeKey())
	require.NoError(t, err)
	require.True(t, live)
	s.SetClock(func() time.Time { return now.Add(16 * time.Minute) })
	live, err = s.WakeWaiterLive(context.Background(), wakeKey())
	require.NoError(t, err)
	require.False(t, live, "an expired lease is no waiter")
	_, err = s.WakeWaiterLive(context.Background(), sqlite.WakeLeaseKey{})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestReopenStalledWake_BudgetHolds_LeavesSentRowUntouched(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/h.db", now)
	key := stallKey(1)
	sentRow(t, s, key)
	s.SetClock(func() time.Time { return now.Add(time.Minute) })
	res, err := s.ReopenStalledWake(context.Background(), key, 30*time.Second, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: false}, nil
	})
	require.NoError(t, err)
	require.False(t, res.Won)
	require.True(t, res.Held, "the budget refused the reopen")
	got, _, err := s.ReadWakeResult(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeSent, got.Outcome, "a refused reopen writes nothing")
}

func TestStalledWakeCandidates_ListsOnlyUnackedSentRowsOfThisHost(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/i.db", now)
	for seq := int64(1); seq <= 3; seq++ {
		sentRow(t, s, stallKey(seq))
	}
	_, won, err := s.ClaimWake(context.Background(), stallKey(4), time.Minute)
	require.NoError(t, err)
	require.True(t, won, "a claimed, unsent row is not a candidate")
	_, err = s.WriteDB().ExecContext(context.Background(),
		`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES(?,?,?)`, "TEST-agent", 2, now.Format(time.RFC3339Nano))
	require.NoError(t, err)

	_, err = s.WriteDB().ExecContext(context.Background(),
		`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES(?,?)`, "TEST-agent", 0)
	require.NoError(t, err)
	keys, err := s.StalledWakeCandidates(context.Background(), sqlite.StalledWakeQuery{HostID: "TEST-host", UID: "1000", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []model.WakeKey{stallKey(1), stallKey(3)}, keys, "event 2 is acknowledged")
	keys, err = s.StalledWakeCandidates(context.Background(), sqlite.StalledWakeQuery{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, []model.WakeKey{stallKey(1)}, keys, "an empty scope does not narrow, and the limit bounds")
	keys, err = s.StalledWakeCandidates(context.Background(), sqlite.StalledWakeQuery{HostID: "OTHER-host", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, keys)
	_, err = s.StalledWakeCandidates(context.Background(), sqlite.StalledWakeQuery{})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestStalledWakeCandidates_CursorAboveRows_ExcludesThem(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/cur.db", now)
	for seq := int64(1); seq <= 3; seq++ {
		sentRow(t, s, stallKey(seq))
	}
	_, err := s.WriteDB().ExecContext(context.Background(),
		`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES(?,?)`, "TEST-agent", 2)
	require.NoError(t, err)
	keys, err := s.StalledWakeCandidates(context.Background(), sqlite.StalledWakeQuery{HostID: "TEST-host", UID: "1000", Limit: 10})
	require.NoError(t, err)
	require.Equal(t, []model.WakeKey{stallKey(3)}, keys, "events the agent's cursor already covers were read")
}

func TestStalledWakeCandidates_HarnessFilterAndKeysetPaging(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/page.db", now)
	for seq := int64(1); seq <= 3; seq++ {
		sentRow(t, s, stallKey(seq))
	}
	q := sqlite.StalledWakeQuery{HostID: "TEST-host", UID: "1000", Limit: 10, Harness: "hook-harness"}
	keys, err := s.StalledWakeCandidates(context.Background(), q)
	require.NoError(t, err)
	require.Empty(t, keys, "no registration has the harness: nothing is a candidate")
	_, err = s.WriteDB().ExecContext(context.Background(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('TEST-agent','hook-harness','1000','TEST-host','manual',1,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`)
	require.NoError(t, err)
	keys, err = s.StalledWakeCandidates(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, keys, 3)
	q.Limit, q.AfterSeq, q.AfterAgent = 1, 1, "TEST-agent"
	keys, err = s.StalledWakeCandidates(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, []model.WakeKey{stallKey(2)}, keys, "the keyset cursor resumes after the last row examined")
}
