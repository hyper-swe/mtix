// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// faultyHoldStore injects one-shot failures into the claim and mark-read paths.
type faultyHoldStore struct {
	hopTableStore
	claimFailsFor int64
	markFailsFor  int64
}

func (s *faultyHoldStore) ClaimWakeBudget(ctx context.Context, key model.WakeKey, ttl time.Duration,
	decide func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	if key.EventSeq == s.claimFailsFor {
		s.claimFailsFor = 0
		return model.WakeBudgetResult{}, errors.New("injected claim failure")
	}
	return s.Store.ClaimWakeBudget(ctx, key, ttl, decide)
}

func (s *faultyHoldStore) WakeRegistrationMark(ctx context.Context, key model.WakeKey) (model.WakeRegistrationMark, error) {
	if key.EventSeq == s.markFailsFor {
		s.markFailsFor = 0
		return model.WakeRegistrationMark{}, errors.New("injected mark read failure")
	}
	return s.Store.WakeRegistrationMark(ctx, key)
}

type budgetHoldFixture struct {
	*holdFixture
	state *oplocal.State
	now   time.Time
}

func newBudgetHoldFixture(t *testing.T, settings map[string]string, faulty *faultyHoldStore) *budgetHoldFixture {
	t.Helper()
	store, _, alerts := wakeServiceStore(t)
	state := testutil.OperatorState(t)
	for key, value := range settings {
		_, err := wakeConfig(t, state, "", true).Set(key, value)
		require.NoError(t, err)
	}
	f := &budgetHoldFixture{holdFixture: &holdFixture{store: store, key: wakeServiceKey(), hops: map[int64]int64{}}, state: state,
		now: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)}
	_, err := store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,5,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		f.key.AgentID, "claude", f.key.UID, f.key.HostID, "manual", f.key.Epoch)
	require.NoError(t, err)
	var backend service.WakeStore = hopTableStore{store, f.hops}
	if faulty != nil {
		faulty.hopTableStore = hopTableStore{store, f.hops}
		backend = faulty
	}
	f.d, err = service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: backend, Alerts: alerts, Settings: state, BudgetState: state,
		Clock: func() time.Time { return f.now }, Held: func(string) {},
		Send: func(_ context.Context, a model.WakeAttempt) error {
			f.sent = append(f.sent, a.Key.EventSeq)
			return nil
		}})
	require.NoError(t, err)
	return f
}

func TestHold_ClaimError_DoesNotAdvanceMark_RetriedNextPass(t *testing.T) {
	f := newBudgetHoldFixture(t, nil, &faultyHoldStore{claimFailsFor: 7})
	require.Error(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Empty(t, f.sent, "the errored event blocks the later event of its registration")
	hw, _ := f.registration(t)
	require.Equal(t, int64(5), hw, "an event that was never sent must not move the mark")
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Equal(t, []int64{7, 8}, f.sent)
	hw, _ = f.registration(t)
	require.Equal(t, int64(8), hw)
}

func TestHold_MarkReadError_BlocksLaterEventOfSameRegistration(t *testing.T) {
	f := newBudgetHoldFixture(t, nil, &faultyHoldStore{markFailsFor: 7})
	require.Error(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Empty(t, f.sent)
	hw, _ := f.registration(t)
	require.Equal(t, int64(5), hw)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Equal(t, []int64{7, 8}, f.sent)
}

func TestHold_RecipientBudget_HoldsBehindLimitWithoutMovingMark(t *testing.T) {
	f := newBudgetHoldFixture(t, map[string]string{"wake.wakes_per_hour": "1"}, nil)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8), f.at(9)}))
	require.Equal(t, []int64{7}, f.sent)
	hw, hold := f.registration(t)
	require.Equal(t, int64(7), hw, "a budget-held event must not move the mark (I14)")
	require.True(t, hold)
	_, err := wakeConfig(t, f.state, "", true).Set("wake.wakes_per_hour", "5")
	require.NoError(t, err)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(8), f.at(9)}))
	require.Equal(t, []int64{7, 8, 9}, f.sent, "the held event wakes first, then the one behind it")
	hw, hold = f.registration(t)
	require.Equal(t, int64(9), hw)
	require.False(t, hold)
}

func TestHold_Backoff_HoldsBehindDeadlineWithoutMovingMark(t *testing.T) {
	f := newBudgetHoldFixture(t, map[string]string{"wake.backoff_after_unacked": "1"}, nil)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8), f.at(9)}))
	require.Equal(t, []int64{7}, f.sent)
	hw, hold := f.registration(t)
	require.Equal(t, int64(7), hw, "a back-off-held event must not move the mark (I15)")
	require.True(t, hold)
	f.now = f.now.Add(2 * time.Minute)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(8), f.at(9)}))
	require.Equal(t, []int64{7, 8}, f.sent, "after the window the held event wakes before the one behind it")
}

func TestHold_OutOfOrderCandidates_HeldLowerBlocksHigher(t *testing.T) {
	f := newBudgetHoldFixture(t, map[string]string{"wake.max_hop_depth": "1"}, nil)
	f.hops[7] = 3
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(8), f.at(7)}))
	require.Empty(t, f.sent, "event 8 must not overtake the held event 7 although it was listed first")
	hw, _ := f.registration(t)
	require.Equal(t, int64(5), hw)
}
