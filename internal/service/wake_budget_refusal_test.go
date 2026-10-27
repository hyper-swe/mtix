// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

type wakeAdmissionBackend struct{ proposal model.WakeBudgetProposal }

func (b wakeAdmissionBackend) ClaimWakeBudget(_ context.Context, _ model.WakeKey, _ time.Duration,
	admit func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error),
) (model.WakeBudgetResult, error) {
	_, err := admit(b.proposal)
	return model.WakeBudgetResult{}, err
}

type wakeBudgetPolicyReader struct {
	values  map[string]model.WakeSetting
	reports []oplocal.Report
	err     error
}

func (r wakeBudgetPolicyReader) WakeSettings(string) (map[string]model.WakeSetting, []oplocal.Report, error) {
	return r.values, r.reports, r.err
}

func TestWakeBudget_UnsafeAdmissionPreservesProtectedState(t *testing.T) {
	for _, name := range []string{"clock", "source", "negative total", "negative ack", "fence", "unacked overflow", "credit overflow", "rewind overflow", "counter exhausted"} {
		t.Run(name, func(t *testing.T) {
			state, recipient, source := wakePublisherFixture(t)
			key := model.WakeKey{AgentID: recipient.AgentID, HostID: recipient.HostID, UID: recipient.UID, EventSeq: 1}
			now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
			p := model.WakeBudgetProposal{Snapshot: model.WakeBudgetSnapshot{Source: source, Total: 5}, Claimable: true,
				Attempt: model.WakeAttempt{Key: key, Token: "fresh"}}
			mutateUnsafeWakeFixture(t, state, recipient, source, name, &now, &p)
			before, _, err := state.ReadWakeBudget(recipient)
			require.NoError(t, err)
			b := &wakeBudgets{state: state, store: wakeAdmissionBackend{p}, clock: func() time.Time { return now },
				settings: wakeBudgetPolicyReader{values: map[string]model.WakeSetting{"wake.backoff_after_unacked": {Value: "1000"}}}}
			_, won, err := b.claim(t.Context(), key)
			want := model.ErrInvalidInput
			if name == "fence" {
				want = model.ErrConflict
			}
			require.ErrorIs(t, err, want)
			require.False(t, won)
			after, _, err := state.ReadWakeBudget(recipient)
			require.NoError(t, err)
			require.Equal(t, before, after, "refused admission must not checkpoint any new charge")
		})
	}
}

func mutateUnsafeWakeFixture(t *testing.T, state *oplocal.State, key model.WakeRecipient, source, name string,
	now *time.Time, p *model.WakeBudgetProposal,
) {
	t.Helper()
	require.NoError(t, state.UpdateWakeBudget(t.Context(), key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		switch name {
		case "clock":
			*now = time.Time{}
		case "source":
			p.Snapshot.Source = ""
		case "negative total":
			p.Snapshot.Total = -1
		case "negative ack":
			p.Snapshot.AckMark = -1
		case "fence":
			p.Attempt.Token = ""
		case "unacked overflow":
			m.Unacked = math.MaxInt64
			p.Snapshot.Total = 6
		case "credit overflow":
			m.Credits[0].Count = math.MaxInt64
		case "rewind overflow":
			m.Rewinds, p.Snapshot.Total = math.MaxUint64, 0
		case "counter exhausted":
			m.Sources[source] = oplocal.WakeBudgetSource{Total: math.MaxInt64, Committed: math.MaxInt64}
			p.Snapshot.Total = math.MaxInt64
		}
		return save()
	}))
}

func TestWakeBudget_InvalidPolicyStopsBeforeAccounting(t *testing.T) {
	cause := errors.New("settings unavailable")
	for _, name := range []string{"read error", "foreign host", "zero backoff", "invalid backoff", "zero limit", "invalid limit"} {
		t.Run(name, func(t *testing.T) {
			state, recipient, _ := wakePublisherFixture(t)
			r := wakeBudgetPolicyReader{values: map[string]model.WakeSetting{"wake.backoff_after_unacked": {Value: "5"}}}
			want := model.ErrInvalidInput
			switch name {
			case "read error":
				r.err, want = cause, cause
			case "foreign host":
				r.reports, want = []oplocal.Report{{Name: "wake", Reason: "foreign"}}, model.ErrOperatorStateUnreadable
			case "zero backoff":
				r.values["wake.backoff_after_unacked"] = model.WakeSetting{Value: "0"}
			case "invalid backoff":
				r.values["wake.backoff_after_unacked"] = model.WakeSetting{Value: "invalid"}
			case "zero limit":
				r.values["wake.wakes_per_hour"] = model.WakeSetting{Value: "0"}
			case "invalid limit":
				r.values["wake.wakes_per_hour"] = model.WakeSetting{Value: "invalid"}
			}
			b := &wakeBudgets{state: state, settings: r} // No store: policy refusal must precede all accounting I/O.
			_, won, err := b.claim(t.Context(), model.WakeKey{AgentID: recipient.AgentID})
			require.ErrorIs(t, err, want)
			require.False(t, won)
		})
	}
}
