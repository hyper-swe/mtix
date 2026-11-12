// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that stalled-turn recovery (MTIX-112.40.12, FR-20) is driven by the wake ledger and
// not by the keys the scanner passes, and that it obeys the gates a first wake obeys. The
// registration row is real: its high-water mark is already past the stalled event, which is
// the production shape the scanner leaves behind.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// markedStallDispatcher builds a push-route dispatcher scoped to the recipient's host, with
// a registration whose mark is past every event and the given wake settings applied.
func markedStallDispatcher(t *testing.T, st *sqlite.Store, alerts *service.Alerts, tally *wakeTally, settings map[string]string) *service.WakeDispatcher {
	t.Helper()
	return markedStallDispatcherOn(t, st, st, alerts, tally, settings)
}

// hopStore reports a chain depth past the default limit once deep is set, so a test can
// let the first wake through and trip the hop guard on the redelivery.
type hopStore struct {
	*sqlite.Store
	deep bool
}

func (h *hopStore) WakeEventHop(ctx context.Context, key model.WakeKey) (int64, error) {
	if h.deep {
		return 100, nil
	}
	return h.Store.WakeEventHop(ctx, key)
}

// markedStallDispatcherOn is markedStallDispatcher with the dispatcher reading backing.
func markedStallDispatcherOn(t *testing.T, st *sqlite.Store, backing service.WakeStore, alerts *service.Alerts, tally *wakeTally, settings map[string]string) *service.WakeDispatcher {
	t.Helper()
	state := testutil.OperatorState(t)
	for key, value := range settings {
		_, err := wakeConfig(t, state, "", true).Set(key, value)
		require.NoError(t, err)
	}
	_, err := st.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,?,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		bothRoutesRecipient.AgentID, "claude", bothRoutesRecipient.UID, bothRoutesRecipient.HostID, "manual", 1, int64(pushStallEvents))
	require.NoError(t, err)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: backing, Alerts: alerts, Adapters: []service.WakeAdapter{tallySocket{tally}},
		Settings: state, BudgetState: state, Clock: time.Now, Held: func(string) {},
		HostID: bothRoutesRecipient.HostID, UID: bothRoutesRecipient.UID,
		Stall: &service.WakeStallDeps{
			Window:     func(string) (time.Duration, error) { return stallWindow, nil },
			WaiterLive: func(context.Context, model.WakeKey) (bool, error) { return false, nil },
			HookRoute: func(ctx context.Context, key model.WakeKey) (bool, error) {
				regs, err := st.ListRegistrations(ctx, key.HostID, key.UID)
				for _, r := range regs {
					if r.AgentID == key.AgentID {
						return r.Harness == "claude", err
					}
				}
				return false, err
			},
		},
	})
	require.NoError(t, err)
	return d
}

// sentBeforeMark has the hook waiter wake the session for event 1, then lets the scanner
// pass the later events so the registration mark moves past all of them.
func sentBeforeMark(t *testing.T, st *sqlite.Store, d *service.WakeDispatcher, tally *wakeTally) {
	t.Helper()
	stallHookWake(t, st, tally)
	keys := make([]model.WakeKey, 0, pushStallEvents)
	for seq := int64(1); seq <= pushStallEvents; seq++ {
		keys = append(keys, bothRoutesKey(seq))
	}
	require.NoError(t, d.Pass(t.Context(), keys))
}

func TestStalledRecovery_MarkPastEvent_NoKeysPassed_RedeliversExactlyOnce(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, nil)
	sentBeforeMark(t, st, d, tally)
	require.Equal(t, 1, tally.wakes[1])

	require.NoError(t, d.Pass(t.Context(), nil), "within the window nothing is redelivered")
	require.Equal(t, 1, tally.wakes[1])

	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil), "an empty candidate set still recovers the stalled event")
	require.Equal(t, 2, tally.wakes[1], "redelivered once through the push route")
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 2, tally.wakes[1], "the redelivery is itself within the window now")

	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 2, tally.wakes[1], "the retry cap is one")
	got, _, err := st.ReadWakeResult(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.Equal(t, model.WakeReasonStallUnconfirmed, got.Reason)
}

func TestStalledRecovery_AcknowledgedEvent_IsNotRedelivered(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, nil)
	sentBeforeMark(t, st, d, tally)
	// The fixture events are synthetic (no journal), so the selective ack is written as the
	// row InboxAck would leave: the candidate query and the reopen both read this table.
	_, err := st.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_inbox_ack (agent_id, event_seq, acked_at) VALUES (?, ?, ?)`,
		bothRoutesRecipient.AgentID, int64(1), now.Format(time.RFC3339))
	require.NoError(t, err)
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 1, tally.wakes[1], "the session read it: no second push")
}

func TestStalledRecovery_NonHookRegistration_IsNeverRedelivered(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, nil)
	sentBeforeMark(t, st, d, tally)
	_, err := st.WriteDB().ExecContext(t.Context(), `UPDATE agent_registrations SET harness='other' WHERE agent_id=?`, bothRoutesRecipient.AgentID)
	require.NoError(t, err)
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 1, tally.wakes[1], "a registration that is not on the hook route has no stalled turn to recover")
}

func TestStalledRecovery_HopDepthPastLimit_IsHeldNotRedelivered(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	hs := &hopStore{Store: st}
	d := markedStallDispatcherOn(t, st, hs, alerts, tally, nil)
	sentBeforeMark(t, st, d, tally)
	hs.deep = true
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 1, tally.wakes[1], "a chain past wake.max_hop_depth is not redelivered by the recovery either")
	got, _, err := st.ReadWakeResult(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.Equal(t, model.WakeSent, got.Outcome, "the held recovery wrote nothing")
}

func TestStalledRecovery_RecipientBudgetSpent_IsHeldNotRedelivered(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, map[string]string{"wake.wakes_per_hour": "1"})
	stallHookWake(t, st, tally)
	_, err := st.WriteDB().ExecContext(t.Context(), `UPDATE agent_registrations SET high_water=1 WHERE agent_id=?`, bothRoutesRecipient.AgentID)
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{bothRoutesKey(2), bothRoutesKey(3)}))
	require.Equal(t, 1, tally.wakes[2], "the one wake of the hour went to a fresh event")
	require.Zero(t, tally.wakes[3], "the budget is spent")
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), nil))
	require.Equal(t, 1, tally.wakes[1], "the redelivery obeys the same hourly budget as a first wake")
}

func TestStalledRecovery_LiveWakeOff_PushesNothingAndWritesNothing(t *testing.T) {
	st, now, alerts := wakeServiceStore(t)
	tally := &wakeTally{wakes: map[int64]int{}}
	d := markedStallDispatcher(t, st, alerts, tally, map[string]string{"wake.live": "false"})
	stallHookWake(t, st, tally)
	*now = now.Add(2 * stallWindow)
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{bothRoutesKey(1), bothRoutesKey(2)}))
	require.Equal(t, 1, tally.wakes[1], "wake.live off: no second push of the stalled event")
	require.Zero(t, tally.wakes[2], "wake.live off: no push at all")
	got, _, err := st.ReadWakeResult(t.Context(), bothRoutesKey(1))
	require.NoError(t, err)
	require.Equal(t, model.WakeSent, got.Outcome, "no claim row change either")
}

func TestStalledRecovery_ReclaimStalledAdmissionRefusal_WritesNothing(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	seq := addressedSeq(t, st)
	key := bothRoutesKey(seq)
	c := stallClaimer(t, st)
	attempt, _, err := c.Claim(t.Context(), key)
	require.NoError(t, err)
	require.NoError(t, st.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}))
	*now = now.Add(2 * stallWindow)
	_, won, err := c.ReclaimStalled(t.Context(), key, stallWindow,
		func(_ context.Context, _ model.WakeKey, reopen func(service.WakeAdmitFunc) (model.WakeBudgetResult, error)) (model.WakeAttempt, bool, error) {
			res, rerr := reopen(func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
				return model.WakeBudgetDecision{Allow: false}, nil
			})
			return res.Attempt, res.Won, rerr
		})
	require.NoError(t, err)
	require.False(t, won)
	got, _, err := st.ReadWakeResult(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeSent, got.Outcome)
}
