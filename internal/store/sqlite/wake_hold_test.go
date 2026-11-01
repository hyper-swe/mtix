// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeRegistrationMark_MissingRow_ReportsNotFound(t *testing.T) {
	s := newTestStore(t)
	mark, err := s.WakeRegistrationMark(t.Context(), model.WakeKey{AgentID: "a", HostID: "h", UID: "u"})
	require.NoError(t, err)
	require.False(t, mark.Found)
}

func TestSetWakeHold_AdvancesMonotonicallyAndGuardsEpoch(t *testing.T) {
	s := newTestStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('a','claude','u','h','manual',2,10,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','t',1)`)
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", Epoch: 2}
	require.NoError(t, s.SetWakeHold(t.Context(), key, 0, true))
	mark, err := s.WakeRegistrationMark(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeRegistrationMark{Found: true, Epoch: 2, HighWater: 10, Held: true}, mark)
	require.NoError(t, s.SetWakeHold(t.Context(), key, 4, false))
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(10), mark.HighWater, "the mark never moves back")
	require.True(t, mark.Held, "an advance below the mark is stale and must not clear the hold")
	require.NoError(t, s.SetWakeHold(t.Context(), key, 12, false))
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(12), mark.HighWater)
	require.False(t, mark.Held)
	stale := key
	stale.Epoch = 1
	require.NoError(t, s.SetWakeHold(t.Context(), stale, 99, true))
	require.NoError(t, s.SetWakeHold(t.Context(), stale, 99, false), "a stale-epoch advance is a no-op, not an error")
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(12), mark.HighWater)
	require.False(t, mark.Held, "a stale epoch cannot change a re-registered row")
}

func TestHeldWakeCandidates_ExactKeysOwnMarkEpochHostAndFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))
	var notes []model.Annotation
	for i, addressee := range []string{"a", "a", "a", "b", "b", "c"} {
		notes = append(notes, model.Annotation{ID: "01ANN" + string(rune('A'+i)), Author: "sender", Text: "n", CreatedAt: now.Add(time.Duration(i) * time.Second), Addressee: addressee})
		require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", notes)) // one journal event per new annotation
	}
	seqs := map[string][]int64{}
	for _, agent := range []string{"a", "b", "c"} {
		events, err := s.InboxList(ctx, agent)
		require.NoError(t, err)
		for _, event := range events {
			seqs[agent] = append(seqs[agent], event.Seq)
		}
	}
	require.Len(t, seqs["a"], 3)
	require.Len(t, seqs["b"], 2)
	insert := func(agent, host, uid string, epoch, highWater, hold int64) {
		_, err := s.WriteDB().ExecContext(ctx, `INSERT INTO agent_registrations
			(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
			VALUES (?,'claude',?,?,'manual',?,?,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','t',1)`, agent, uid, host, epoch, highWater, hold)
		require.NoError(t, err)
	}
	insert("a", "h", "u", 3, seqs["a"][1], 1) // own mark is the second event
	insert("b", "h", "u", 2, seqs["b"][0], 1) // own mark is above every event of a
	insert("c", "h", "u", 1, 0, 0)            // not held
	insert("a", "h", "other-uid", 9, 0, 1)    // another operating-system user
	insert("a", "other-host", "u", 9, 0, 1)   // another host
	key := func(agent string, epoch, seq int64) model.WakeKey {
		return model.WakeKey{AgentID: agent, HostID: "h", UID: "u", EventSeq: seq, Epoch: epoch}
	}
	all, err := s.HeldWakeCandidates(ctx, "h", "u", "")
	require.NoError(t, err)
	require.Equal(t, []model.WakeKey{key("a", 3, seqs["a"][2]), key("b", 2, seqs["b"][1])}, all,
		"only held rows of this host and uid, only events above each row's own mark, with the row's epoch")
	onlyB, err := s.HeldWakeCandidates(ctx, "h", "u", "b")
	require.NoError(t, err)
	require.Equal(t, []model.WakeKey{key("b", 2, seqs["b"][1])}, onlyB)
	notHeld, err := s.HeldWakeCandidates(ctx, "h", "u", "c")
	require.NoError(t, err)
	require.Empty(t, notHeld, "a registration without a pending hold is never a candidate")
	none, err := s.HeldWakeCandidates(ctx, "h", "u", "nobody")
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestSetWakeHold_StaleAdvance_DoesNotClearPendingHold(t *testing.T) {
	s := newTestStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('a','claude','u','h','manual',2,10,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','t',1)`)
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", Epoch: 2}
	for _, to := range []int64{4, 10} {
		require.NoError(t, s.SetWakeHold(t.Context(), key, to, false))
		mark, mErr := s.WakeRegistrationMark(t.Context(), key)
		require.NoError(t, mErr)
		require.True(t, mark.Held, "an advance that does not pass the mark must not clear the hold")
		require.Equal(t, int64(10), mark.HighWater)
	}
	require.NoError(t, s.SetWakeHold(t.Context(), key, 11, false))
	mark, _ := s.WakeRegistrationMark(t.Context(), key)
	require.False(t, mark.Held)
}

func TestHeldWakeEvent_LowestUnackedAboveMark(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))
	var notes []model.Annotation
	for i := 0; i < 3; i++ {
		notes = append(notes, model.Annotation{ID: "01ANN" + string(rune('A'+i)), Author: "sender", Text: "n", CreatedAt: now.Add(time.Duration(i) * time.Second), Addressee: "a"})
		require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", notes))
	}
	events, err := s.InboxList(ctx, "a")
	require.NoError(t, err)
	require.Len(t, events, 3)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u"}
	seq, found, err := s.HeldWakeEvent(ctx, key, 0)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, events[0].Seq, seq)
	seq, found, _ = s.HeldWakeEvent(ctx, key, events[0].Seq)
	require.True(t, found)
	require.Equal(t, events[1].Seq, seq)
	_, found, err = s.HeldWakeEvent(ctx, key, events[2].Seq)
	require.NoError(t, err)
	require.False(t, found)
	_, _, err = s.HeldWakeEvent(ctx, model.WakeKey{}, 0)
	require.Error(t, err)
}

func TestSetWakeHold_AdvancePastHeldEvent_RefusedWithoutMovingMarkOrHold(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "Task", now)))
	var notes []model.Annotation
	for i := 0; i < 3; i++ {
		notes = append(notes, model.Annotation{ID: "01ANN" + string(rune('A'+i)), Author: "sender", Text: "n", CreatedAt: now.Add(time.Duration(i) * time.Second), Addressee: "a"})
		require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", notes))
	}
	events, err := s.InboxList(ctx, "a")
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(ctx, `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('a','claude','u','h','manual',1,0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','t',1)`)
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", Epoch: 1}
	err = s.SetWakeHold(ctx, key, events[1].Seq, false)
	require.ErrorIs(t, err, model.ErrWakeHeldEventMissing)
	mark, _ := s.WakeRegistrationMark(ctx, key)
	require.True(t, mark.Held)
	require.Zero(t, mark.HighWater)
	// Advancing exactly to the lowest event (the held one itself) is allowed and lifts the hold.
	require.NoError(t, s.SetWakeHold(ctx, key, events[0].Seq, false))
	mark, _ = s.WakeRegistrationMark(ctx, key)
	require.False(t, mark.Held)
	require.Equal(t, events[0].Seq, mark.HighWater)
	// The held event sits exactly one above the mark: the guard must still see it.
	_, err = s.WriteDB().ExecContext(ctx,
		"UPDATE agent_registrations SET high_water = ?, pending_hold = 1 WHERE agent_id = 'a'", events[1].Seq-1)
	require.NoError(t, err)
	require.ErrorIs(t, s.SetWakeHold(ctx, key, events[2].Seq, false), model.ErrWakeHeldEventMissing)
}

func TestSetWakeHold_GuardInboxReadFails_FailsClosedWithoutMovingMarkOrHold(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	_, err := s.WriteDB().ExecContext(ctx, `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('a','claude','u','h','manual',1,5,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','t',1)`)
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", Epoch: 1}
	// The UPDATE would succeed; only the guard's inbox query breaks (its ack table is gone).
	_, err = s.WriteDB().ExecContext(ctx, "ALTER TABLE agent_inbox_ack RENAME TO agent_inbox_ack_gone")
	require.NoError(t, err)
	err = s.SetWakeHold(ctx, key, 9, false)
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrWakeHeldEventMissing)
	_, err = s.WriteDB().ExecContext(ctx, "ALTER TABLE agent_inbox_ack_gone RENAME TO agent_inbox_ack")
	require.NoError(t, err)
	mark, err := s.WakeRegistrationMark(ctx, key)
	require.NoError(t, err)
	require.True(t, mark.Held, "a failed guard must not clear the hold")
	require.Equal(t, int64(5), mark.HighWater, "a failed guard must not move the mark")
}
