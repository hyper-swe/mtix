// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// addressedEvents journals n events addressed to the fixture agent and returns their seqs.
func (f *holdFixture) addressedEvents(t *testing.T, n int) []int64 {
	t.Helper()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	node := &model.Node{ID: "PROJ-1", Project: "PROJ", Seq: 1, Title: "Task", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeStory, Creator: "sender",
		ContentHash: model.ComputeContentHash("Task", "", "", "", nil), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, f.store.CreateNode(t.Context(), node))
	var notes []model.Annotation
	for i := 0; i < n; i++ {
		notes = append(notes, model.Annotation{ID: "01ANN" + string(rune('A'+i)), Author: "sender", Text: "n",
			CreatedAt: now.Add(time.Duration(i) * time.Second), Addressee: f.key.AgentID})
		require.NoError(t, f.store.SetAnnotations(t.Context(), "PROJ-1", notes))
	}
	events, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	var seqs []int64
	for _, e := range events {
		seqs = append(seqs, e.Seq)
	}
	require.Len(t, seqs, n)
	return seqs
}

// The reviewer's probe: event 7 held in one pass, a later pass that omits it must not
// advance the mark past it (ADR-007 section 2.7, FR-20.3).
func TestPass_HeldEventOmitted_DoesNotAdvanceMarkPastIt(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 2)
	f.hops[seqs[0]] = 3 // over the hop limit of 1: held
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[0])}))
	hw, hold := f.registration(t)
	require.Zero(t, hw)
	require.True(t, hold)

	err := f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[1])})
	require.ErrorIs(t, err, model.ErrWakeHeldEventMissing)
	require.Empty(t, f.sent, "the later event must not overtake the held one")
	hw, hold = f.registration(t)
	require.Zero(t, hw, "the mark must stay below the held event")
	require.True(t, hold, "the hold must stay pending")
}

func TestPass_HeldEventIncluded_StillBlocksLaterEvent(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 2)
	f.hops[seqs[0]] = 3
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[0])}))
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[1]), f.at(seqs[0])}))
	require.Empty(t, f.sent)
	hw, hold := f.registration(t)
	require.Zero(t, hw)
	require.True(t, hold)
}

func TestPass_HeldLifted_WakesBothInOrder(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 2)
	f.hops[seqs[0]] = 3
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[0])}))
	f.hops[seqs[0]] = 0
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[1]), f.at(seqs[0])}))
	require.Equal(t, seqs, f.sent)
	hw, hold := f.registration(t)
	require.Equal(t, seqs[1], hw)
	require.False(t, hold)
}

// setMark places the fixture registration's mark and hold directly.
func (f *holdFixture) setMark(t *testing.T, mark int64, hold bool) {
	t.Helper()
	_, err := f.store.WriteDB().ExecContext(t.Context(),
		"UPDATE agent_registrations SET high_water = ?, pending_hold = ? WHERE agent_id = ?", mark, hold, f.key.AgentID)
	require.NoError(t, err)
}

// The reviewer's interleaving: the hold on the lower event is recorded while the higher
// event of an omitting pass is sending. The advance must be refused in its own transaction,
// leaving the hold and the mark, and the lower event is woken by a complete later pass.
func TestPass_HoldRecordedWhileHigherEventSends_AdvanceRefusedAndLowerWokenLater(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 2)
	f.onSend = func(int64) {
		require.NoError(t, f.store.SetWakeHold(t.Context(), f.at(seqs[0]), 0, true))
		f.onSend = nil
	}
	err := f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[1])})
	require.ErrorIs(t, err, model.ErrWakeHeldEventMissing)
	hw, hold := f.registration(t)
	require.Zero(t, hw, "the mark must not pass the held event")
	require.True(t, hold, "the fresh hold must survive")
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[0]), f.at(seqs[1])}))
	require.Equal(t, []int64{seqs[1], seqs[0]}, f.sent, "the held event is woken by the later complete pass")
	hw, hold = f.registration(t)
	require.Equal(t, seqs[1], hw)
	require.False(t, hold)
}

// Concurrent omitting and complete passes over a held registration never lift the hold
// or move the mark (run under -race).
func TestPass_ConcurrentOmittingAndCompletePasses_HoldAndMarkStay(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 2)
	f.hops[seqs[0]] = 3
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[0])}))
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		keys := []model.WakeKey{f.at(seqs[1])}
		if i%2 == 1 {
			keys = []model.WakeKey{f.at(seqs[1]), f.at(seqs[0])}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.d.Pass(t.Context(), keys); err != nil && !errors.Is(err, model.ErrWakeHeldEventMissing) {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	hw, hold := f.registration(t)
	assert.Zero(t, hw)
	assert.True(t, hold)
}

func TestPass_HeldEventVariants_RefuseOrProceed(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, f *holdFixture, seqs []int64)
		wantErr  bool
		wantSent bool
	}{
		{"held event at mark plus one", func(t *testing.T, f *holdFixture, seqs []int64) { f.setMark(t, seqs[0]-1, true) }, true, false},
		{"held event acked meanwhile", func(t *testing.T, f *holdFixture, seqs []int64) {
			f.setMark(t, 0, true)
			require.NoError(t, f.store.InboxAck(t.Context(), f.key.AgentID, seqs[0]))
		}, false, true},
		{"hold flag clear lets a later event through", func(t *testing.T, f *holdFixture, seqs []int64) { f.setMark(t, 0, false) }, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHoldFixture(t, "1", 0)
			seqs := f.addressedEvents(t, 3)
			tt.setup(t, f, seqs)
			err := f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[1])})
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrWakeHeldEventMissing)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantSent, len(f.sent) == 1)
			if tt.wantSent {
				hw, hold := f.registration(t)
				require.Equal(t, seqs[1], hw)
				require.False(t, hold)
			}
		})
	}
}

func TestPass_ConsecutiveOmittingPasses_EachRefusedMarkUnchanged(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 3)
	f.hops[seqs[0]] = 3
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[0])}))
	for _, omit := range [][]model.WakeKey{{f.at(seqs[1])}, {f.at(seqs[2])}, {f.at(seqs[2]), f.at(seqs[1])}} {
		require.ErrorIs(t, f.d.Pass(t.Context(), omit), model.ErrWakeHeldEventMissing)
	}
	require.Empty(t, f.sent)
	hw, hold := f.registration(t)
	require.Zero(t, hw)
	require.True(t, hold)
}

func TestPass_TwoRegistrationsOfOneAgent_OnlyHeldOneRefuses(t *testing.T) {
	f := newHoldFixture(t, "1", 0)
	seqs := f.addressedEvents(t, 2)
	_, err := f.store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		f.key.AgentID, "claude", f.key.UID, "other-host", "manual", f.key.Epoch)
	require.NoError(t, err)
	f.setMark(t, 0, true) // updates both rows: reset the other one below
	_, err = f.store.WriteDB().ExecContext(t.Context(),
		"UPDATE agent_registrations SET pending_hold = 0 WHERE host_id = 'other-host'")
	require.NoError(t, err)
	other := f.at(seqs[1])
	other.HostID = "other-host"
	require.ErrorIs(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(seqs[1])}), model.ErrWakeHeldEventMissing)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{other}))
	require.Equal(t, []int64{seqs[1]}, f.sent, "the unheld registration of the same agent still wakes")
}
