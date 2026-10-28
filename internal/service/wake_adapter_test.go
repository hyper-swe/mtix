// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests per-registration route selection, claim-once delivery and persisted outcomes
// for the wake adapters (MTIX-112.40.2; ADR-007 section 6.3).
package service_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type fakeAdapter struct {
	route   service.WakeRoute
	live    bool
	healthy bool
	result  model.WakeResult
	err     error
	calls   atomic.Int32
}

func (f *fakeAdapter) Route() service.WakeRoute { return f.route }
func (f *fakeAdapter) Capabilities(model.WakeKey) service.WakeCapabilities {
	return service.WakeCapabilities{Live: f.live}
}
func (f *fakeAdapter) Probe(context.Context, model.WakeKey) service.WakeHealth {
	return service.WakeHealth{Healthy: f.healthy}
}
func (f *fakeAdapter) Contract(context.Context) service.ContractCheck {
	return service.ContractCheck{Matches: true}
}
func (f *fakeAdapter) Deliver(context.Context, model.WakeAttempt) (model.WakeResult, error) {
	f.calls.Add(1)
	return f.result, f.err
}

func armed(route service.WakeRoute) *fakeAdapter {
	return &fakeAdapter{route: route, live: true, healthy: true, result: model.WakeResult{Outcome: model.WakeSent}}
}

type liveSetting struct{ value *atomic.Value }

func newLiveSetting(v string) liveSetting {
	var a atomic.Value
	a.Store(v)
	return liveSetting{&a}
}

func (l liveSetting) set(v string) { l.value.Store(v) }

func (l liveSetting) WakeSettings(string) (map[string]model.WakeSetting, []oplocal.Report, error) {
	return map[string]model.WakeSetting{
		"wake.live": {Value: l.value.Load().(string)}, "wake.max_hop_depth": {Value: "32"},
		"wake.backoff_after_unacked": {Value: "5"},
	}, nil, nil
}

func adapterDispatcher(t *testing.T, opts service.WakeDispatcherOptions) (*service.WakeDispatcher, service.WakeStore) {
	t.Helper()
	s, _, alerts := wakeServiceStore(t)
	opts.Store, opts.Alerts, opts.BudgetState, opts.Clock = s, alerts, testutil.OperatorState(t), time.Now
	d, err := service.NewWakeDispatcher(opts)
	require.NoError(t, err)
	return d, s
}

func readResult(t *testing.T, st service.WakeStore) model.WakeResult {
	t.Helper()
	r, found, err := st.(interface {
		ReadWakeResult(context.Context, model.WakeKey) (model.WakeResult, bool, error)
	}).ReadWakeResult(t.Context(), wakeServiceKey())
	require.NoError(t, err)
	require.True(t, found)
	return r
}

func TestAdapterSelection_Order_PicksFirstLiveHealthy(t *testing.T) {
	tests := []struct {
		name  string
		setup func() (adapters []*fakeAdapter)
		want  service.WakeRoute
	}{
		{"hook waiter beats socket", func() []*fakeAdapter {
			return []*fakeAdapter{armed(service.RouteSocketPush), armed(service.RouteHookWaiter)}
		}, service.RouteHookWaiter},
		{"socket when waiter unhealthy", func() []*fakeAdapter {
			w := armed(service.RouteHookWaiter)
			w.healthy = false
			return []*fakeAdapter{w, armed(service.RouteSocketPush), armed(service.RouteQueue)}
		}, service.RouteSocketPush},
		{"queue when earlier routes not live", func() []*fakeAdapter {
			w, p := armed(service.RouteHookWaiter), armed(service.RouteSocketPush)
			w.live, p.live = false, false
			return []*fakeAdapter{w, p, armed(service.RouteQueue)}
		}, service.RouteQueue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakes := tt.setup()
			adapters := make([]service.WakeAdapter, len(fakes))
			for i, f := range fakes {
				adapters[i] = f
			}
			d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: adapters})
			require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
			total := 0
			for _, f := range fakes {
				total += int(f.calls.Load())
				if f.route == tt.want {
					require.EqualValues(t, 1, f.calls.Load())
				}
			}
			require.Equal(t, 1, total, "exactly one Deliver across all armed routes")
			r := readResult(t, st)
			require.Equal(t, model.WakeSent, r.Outcome)
			require.Equal(t, string(tt.want), r.Route)
			require.NotEmpty(t, r.At)
		})
	}
}

func TestAdapterSelection_LegacySendIsLastRoute(t *testing.T) {
	var legacy atomic.Int32
	send := func(context.Context, model.WakeAttempt) error { legacy.Add(1); return nil }
	sock := armed(service.RouteSocketPush)
	d, _ := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{sock}, Send: send})
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.EqualValues(t, 1, sock.calls.Load())
	require.Zero(t, legacy.Load())

	sock.live = false
	next := wakeServiceKey()
	next.EventSeq++
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{next}))
	require.EqualValues(t, 1, legacy.Load())
}

func TestAdapterSelection_NoLiveRoute_RecordsUndeliverable(t *testing.T) {
	a := armed(service.RouteHookWaiter)
	a.live = false
	d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{a}})
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.Zero(t, a.calls.Load())
	r := readResult(t, st)
	require.Equal(t, model.WakeUndeliverable, r.Outcome)
	require.Equal(t, string(service.RouteInboxOnly), r.Route)
	require.Equal(t, "no-live-route", r.Reason)
}

func TestAdapterDelivery_TwoArmedRoutes_ClaimsOncePerEvent(t *testing.T) {
	w, p := armed(service.RouteHookWaiter), armed(service.RouteSocketPush)
	d, _ := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{w, p}})
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "a lost claim is not an error")
	}
	require.EqualValues(t, 1, w.calls.Load()+p.calls.Load())
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.EqualValues(t, 1, w.calls.Load()+p.calls.Load(), "a later pass never redelivers")
}

func TestAdapterDelivery_LiveWakeOff_SendsNothingAndWritesNoClaim(t *testing.T) {
	a := armed(service.RouteHookWaiter)
	d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{a}, Settings: newLiveSetting("false")})
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.Zero(t, a.calls.Load())
	stats, err := st.(interface {
		WakeLedgerStats(context.Context) (model.WakeLedgerStats, error)
	}).WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Zero(t, stats.Rows)
}

func TestAdapterDelivery_Outcomes_PersistRouteAndReason(t *testing.T) {
	tests := []struct {
		name   string
		result model.WakeResult
		err    error
		want   model.WakeResult
		fails  bool
	}{
		{"held by peer approval", model.WakeResult{Outcome: model.WakeHeld, Reason: "peer-approval"}, nil, model.WakeResult{Outcome: model.WakeHeld, Route: "hook-waiter", Reason: "peer-approval"}, false},
		{"undeliverable keeps reason", model.WakeResult{Outcome: model.WakeUndeliverable, Reason: "session-gone"}, nil, model.WakeResult{Outcome: model.WakeUndeliverable, Route: "hook-waiter", Reason: "session-gone"}, false},
		{"adapter off outcome", model.WakeResult{Outcome: model.WakeOff}, nil, model.WakeResult{Outcome: model.WakeOff, Route: "hook-waiter"}, false},
		{"deliver error is undeliverable", model.WakeResult{}, errors.New("socket refused"), model.WakeResult{Outcome: model.WakeUndeliverable, Route: "hook-waiter", Reason: "deliver-error"}, true},
		{"confirmed is not a delivery outcome", model.WakeResult{Outcome: model.WakeConfirmed}, nil, model.WakeResult{Outcome: model.WakeUndeliverable, Route: "hook-waiter", Reason: "invalid-adapter-outcome"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := armed(service.RouteHookWaiter)
			a.result, a.err = tt.result, tt.err
			d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{a}})
			err := d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()})
			if tt.fails {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			got := readResult(t, st)
			got.At = ""
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNewWakeDispatcher_NoSendAndNoAdapters_Refused(t *testing.T) {
	s, _, alerts := wakeServiceStore(t)
	_, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: s, Alerts: alerts, BudgetState: testutil.OperatorState(t), Clock: time.Now})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestAdapterDelivery_SettingsNil_OperatorLiveOff_SendsNothing(t *testing.T) {
	a := armed(service.RouteHookWaiter)
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "", true).Set("wake.live", "false")
	require.NoError(t, err)
	s, _, alerts := wakeServiceStore(t)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: s, Alerts: alerts, BudgetState: state, Clock: time.Now, Adapters: []service.WakeAdapter{a}})
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	require.Zero(t, a.calls.Load())
	stats, err := s.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Zero(t, stats.Rows)
}

// An off pass is not an attempt: the registration's mark and hold do not move, so the
// same event is nudged once live wake is switched back on.
func TestAdapterDelivery_LiveWakeOffThenOn_EventStillEligible(t *testing.T) {
	a := armed(service.RouteHookWaiter)
	live := newLiveSetting("false")
	d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{a}, Settings: live})
	key := wakeServiceKey()
	db := st.(interface {
		WriteDB() *sql.DB
		ReadDB() *sql.DB
	})
	_, err := db.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,3,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		key.AgentID, "claude", key.UID, key.HostID, "manual", key.Epoch)
	require.NoError(t, err)
	registration := func() (int64, int64) {
		var hw, hold int64
		require.NoError(t, db.ReadDB().QueryRowContext(t.Context(),
			"SELECT high_water, pending_hold FROM agent_registrations WHERE agent_id=?", key.AgentID).Scan(&hw, &hold))
		return hw, hold
	}

	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
	require.Zero(t, a.calls.Load())
	hw, hold := registration()
	require.EqualValues(t, 3, hw, "an off pass does not advance the mark")
	require.EqualValues(t, 1, hold, "an off pass does not lift the hold")

	live.set("true")
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
	require.EqualValues(t, 1, a.calls.Load())
	require.Equal(t, model.WakeSent, readResult(t, st).Outcome)
	hw, hold = registration()
	require.EqualValues(t, key.EventSeq, hw)
	require.Zero(t, hold)
}

func TestAdapterDelivery_MalformedResults_NeverLeaveClaimReclaimable(t *testing.T) {
	tests := []struct {
		name   string
		result model.WakeResult
	}{
		{"undeliverable without reason", model.WakeResult{Outcome: model.WakeUndeliverable}},
		{"delivered from non-legacy adapter", model.WakeResult{Outcome: model.WakeDelivered}},
		{"error from non-legacy adapter", model.WakeResult{Outcome: model.WakeError}},
		{"claimed", model.WakeResult{Outcome: model.WakeClaimed}},
		{"empty", model.WakeResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := armed(service.RouteSocketPush)
			a.result = tt.result
			d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Adapters: []service.WakeAdapter{a}})
			require.ErrorIs(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}), model.ErrInvalidInput)
			got := readResult(t, st)
			require.Equal(t, model.WakeUndeliverable, got.Outcome)
			require.Equal(t, "invalid-adapter-outcome", got.Reason)
			require.Equal(t, string(service.RouteSocketPush), got.Route)
			require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
			require.EqualValues(t, 1, a.calls.Load(), "the finalized claim is never redelivered")
		})
	}
}

func TestAdapterDelivery_LegacySend_WritesUnchangedLegacyRow(t *testing.T) {
	send := func(context.Context, model.WakeAttempt) error { return nil }
	d, st := adapterDispatcher(t, service.WakeDispatcherOptions{Send: send})
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
	var outcome, route, reason, at string
	require.NoError(t, st.(interface{ ReadDB() *sql.DB }).ReadDB().QueryRow(
		"SELECT outcome,route,reason,outcome_at FROM wake_ledger WHERE event_seq=?", 7).Scan(&outcome, &route, &reason, &at))
	require.Equal(t, "delivered", outcome)
	require.Empty(t, route+reason+at)
}
