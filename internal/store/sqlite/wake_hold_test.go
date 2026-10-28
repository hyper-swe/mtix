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
	require.False(t, mark.Held)
	require.NoError(t, s.SetWakeHold(t.Context(), key, 12, false))
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(12), mark.HighWater)
	stale := key
	stale.Epoch = 1
	require.NoError(t, s.SetWakeHold(t.Context(), stale, 99, true))
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
