// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// scanAdapter is a delivery route that records each delivered event and can block inside Deliver.
type scanAdapter struct {
	mu        sync.Mutex
	delivered []int64
	live      atomic.Bool
	onDeliver func(seq int64)
}

func (a *scanAdapter) Route() service.WakeRoute { return service.RouteSocketPush }
func (a *scanAdapter) Capabilities(model.WakeKey) service.WakeCapabilities {
	return service.WakeCapabilities{Live: a.live.Load()}
}
func (a *scanAdapter) Probe(context.Context, model.WakeKey) service.WakeHealth {
	return service.WakeHealth{Healthy: true}
}
func (a *scanAdapter) Contract(context.Context) service.ContractCheck {
	return service.ContractCheck{Matches: true}
}
func (a *scanAdapter) Deliver(_ context.Context, at model.WakeAttempt) (model.WakeResult, error) {
	a.mu.Lock()
	a.delivered = append(a.delivered, at.Key.EventSeq)
	hook := a.onDeliver
	a.mu.Unlock()
	if hook != nil {
		hook(at.Key.EventSeq)
	}
	return model.WakeResult{Outcome: model.WakeSent}, nil
}

func (a *scanAdapter) seqs() []int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]int64(nil), a.delivered...)
}

// scanLiveness reports a settable liveness for every registration.
type scanLiveness struct{ state atomic.Int32 }

func (l *scanLiveness) Probe(context.Context, sqlite.Registration) service.LiveState {
	return service.LiveState(l.state.Load())
}

// scanStore lets a test fail one claim and set hop depths without touching the real store.
type scanStore struct {
	*sqlite.Store
	mu       sync.Mutex
	hops     map[int64]int64
	failSeq  int64
	failOnce bool
	failMark bool
}

func (s *scanStore) WakeEventHop(_ context.Context, key model.WakeKey) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hops[key.EventSeq], nil
}

func (s *scanStore) ClaimWakeBudget(ctx context.Context, key model.WakeKey, lease time.Duration,
	prepare func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error)) (model.WakeBudgetResult, error) {
	s.mu.Lock()
	fail := s.failSeq == key.EventSeq && s.failOnce
	if fail {
		s.failOnce = false
	}
	s.mu.Unlock()
	if fail {
		return model.WakeBudgetResult{}, fmt.Errorf("injected claim failure: %w", model.ErrConflict)
	}
	return s.Store.ClaimWakeBudget(ctx, key, lease, prepare)
}

type scanFixture struct {
	t        *testing.T
	store    *sqlite.Store
	wstore   *scanStore
	state    *oplocal.State
	d        *service.WakeDispatcher
	adapter  *scanAdapter
	liveness *scanLiveness
	key      model.WakeKey
	notes    []model.Annotation
	wakeable atomic.Bool
	gate     func(context.Context, model.WakeKey) (bool, error)
	alerts   *service.Alerts
}

func newScanFixture(t *testing.T) *scanFixture {
	t.Helper()
	store, _, alerts := wakeServiceStore(t)
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "", true).Set("wake.max_hop_depth", "1")
	require.NoError(t, err)
	f := &scanFixture{t: t, alerts: alerts, store: store, state: state, key: wakeServiceKey(), adapter: &scanAdapter{}, liveness: &scanLiveness{}}
	f.liveness.state.Store(int32(service.LiveAlive))
	f.adapter.live.Store(true)
	f.wakeable.Store(true)
	f.wstore = &scanStore{Store: store, hops: map[int64]int64{}}
	f.addRegistration(f.key.AgentID, "claude")
	f.d, err = service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: f.wstore, Alerts: alerts, Adapters: []service.WakeAdapter{f.adapter},
		Settings: state, BudgetState: state, Clock: time.Now, Held: func(string) {}, HostID: f.key.HostID, UID: f.key.UID,
		CandidateFilter: func(ctx context.Context, k model.WakeKey) (bool, error) { return f.gate(ctx, k) }})
	require.NoError(t, err)
	f.gate = func(context.Context, model.WakeKey) (bool, error) { return true, nil }
	node := &model.Node{ID: "PROJ-1", Project: "PROJ", Seq: 1, Title: "Task", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeStory, Creator: "sender",
		ContentHash: model.ComputeContentHash("Task", "", "", "", nil), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	require.NoError(t, store.CreateNode(t.Context(), node))
	return f
}

func (f *scanFixture) addRegistration(agent, harness string) {
	f.t.Helper()
	_, err := f.store.WriteDB().ExecContext(f.t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		agent, harness, f.key.UID, f.key.HostID, "manual", f.key.Epoch)
	require.NoError(f.t, err)
}

// addEvent journals one comment addressed to the fixture agent and returns its inbox seq.
func (f *scanFixture) addEvent(author string) int64 {
	f.t.Helper()
	n := len(f.notes)
	f.notes = append(f.notes, model.Annotation{ID: fmt.Sprintf("01ANN%05d", n), Author: author, Text: "n",
		CreatedAt: time.Date(2026, 10, 15, 12, 0, n, 0, time.UTC), Addressee: f.key.AgentID})
	require.NoError(f.t, f.store.SetAnnotations(f.t.Context(), "PROJ-1", f.notes))
	events, err := f.store.InboxList(f.t.Context(), f.key.AgentID)
	require.NoError(f.t, err)
	require.Len(f.t, events, len(f.notes))
	return events[len(events)-1].Seq
}

func (f *scanFixture) options(writable func() error) service.WakeScannerOptions {
	return service.WakeScannerOptions{
		Store: f.store, Dispatcher: f.d, Liveness: f.liveness, HostID: f.key.HostID, UID: f.key.UID,
		Clock: time.Now, Writable: writable,
		Wakeable: func(context.Context, string) (bool, string, error) {
			if f.wakeable.Load() {
				return true, "test", nil
			}
			return false, "denied", nil
		},
		ExpireStale: func(context.Context) error { return nil },
	}
}

func (f *scanFixture) scanner(writable func() error) *service.WakeScanner {
	f.t.Helper()
	s, err := service.NewWakeScanner(f.options(writable))
	require.NoError(f.t, err)
	return s
}

func (f *scanFixture) ledgerRows() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.store.ReadDB().QueryRowContext(f.t.Context(), "SELECT count(*) FROM wake_ledger").Scan(&n))
	return n
}

func (f *scanFixture) mark() (int64, bool) {
	f.t.Helper()
	var hw, hold int64
	require.NoError(f.t, f.store.ReadDB().QueryRowContext(f.t.Context(),
		"SELECT high_water, pending_hold FROM agent_registrations WHERE agent_id=?", f.key.AgentID).Scan(&hw, &hold))
	return hw, hold == 1
}

func (f *scanFixture) staleSince(agent string) string {
	f.t.Helper()
	var s string
	require.NoError(f.t, f.store.ReadDB().QueryRowContext(f.t.Context(),
		"SELECT COALESCE(stale_since,'') FROM agent_registrations WHERE agent_id=?", agent).Scan(&s))
	return s
}

func always() error { return nil }

func (s *scanStore) WakeRegistrationMark(ctx context.Context, key model.WakeKey) (model.WakeRegistrationMark, error) {
	s.mu.Lock()
	fail := s.failMark
	s.failMark = false
	s.mu.Unlock()
	if fail {
		return model.WakeRegistrationMark{}, fmt.Errorf("injected mark read failure: %w", model.ErrConflict)
	}
	return s.Store.WakeRegistrationMark(ctx, key)
}

// staleFailStore fails the stale marker write.
type staleFailStore struct{ *sqlite.Store }

func (staleFailStore) MarkRegistrationStale(context.Context, sqlite.RegistrationKey, int64, string) (int64, error) {
	return 0, fmt.Errorf("injected stale write failure: %w", model.ErrConflict)
}

// dispatcherOver builds a dispatcher like the fixture's over another store, same route and settings.
func (f *scanFixture) dispatcherOver(store service.WakeStore) *service.WakeDispatcher {
	f.t.Helper()
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: store, Alerts: f.alerts, Adapters: []service.WakeAdapter{f.adapter},
		Settings: f.state, BudgetState: f.state, Clock: time.Now, Held: func(string) {}, HostID: f.key.HostID, UID: f.key.UID,
		CandidateFilter: func(ctx context.Context, k model.WakeKey) (bool, error) { return f.gate(ctx, k) }})
	require.NoError(f.t, err)
	return d
}

// noInboxStore forwards only the dispatcher's own capabilities: it cannot list inbox events.
type noInboxStore struct{ s *scanStore }

func (n noInboxStore) MinDispatchProtocol(c context.Context) (int, error) {
	return n.s.MinDispatchProtocol(c)
}
func (n noInboxStore) WakeEventHop(c context.Context, k model.WakeKey) (int64, error) {
	return n.s.WakeEventHop(c, k)
}
func (n noInboxStore) ClaimWake(c context.Context, k model.WakeKey, d time.Duration) (model.WakeAttempt, bool, error) {
	return n.s.ClaimWake(c, k, d)
}
func (n noInboxStore) RecordWakeOutcome(c context.Context, a model.WakeAttempt, o model.WakeOutcome) error {
	return n.s.RecordWakeOutcome(c, a, o)
}
func (n noInboxStore) RecordWakeResult(c context.Context, a model.WakeAttempt, r model.WakeResult) error {
	return n.s.RecordWakeResult(c, a, r)
}
func (n noInboxStore) ClaimWakeBudget(c context.Context, k model.WakeKey, d time.Duration,
	p func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error)) (model.WakeBudgetResult, error) {
	return n.s.ClaimWakeBudget(c, k, d, p)
}
func (n noInboxStore) HeldWakeCandidates(c context.Context, h, u, a string) ([]model.WakeKey, error) {
	return n.s.HeldWakeCandidates(c, h, u, a)
}
func (n noInboxStore) WakeRegistrationMark(c context.Context, k model.WakeKey) (model.WakeRegistrationMark, error) {
	return n.s.WakeRegistrationMark(c, k)
}
func (n noInboxStore) SetWakeHold(c context.Context, k model.WakeKey, to int64, hold bool) error {
	return n.s.SetWakeHold(c, k, to, hold)
}
func (n noInboxStore) HeldWakeEvent(c context.Context, k model.WakeKey, above int64) (int64, bool, error) {
	return n.s.HeldWakeEvent(c, k, above)
}

// inboxOverrideStore replaces the author read that re-evaluation makes after the candidate list.
type inboxOverrideStore struct {
	*scanStore
	list func() ([]sqlite.InboxEvent, error)
}

func (o inboxOverrideStore) InboxList(context.Context, string) ([]sqlite.InboxEvent, error) {
	return o.list()
}
