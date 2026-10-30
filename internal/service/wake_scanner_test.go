// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// A claimant that is mid-delivery holds the claim; a second CLI-style trigger, a dispatcher
// trigger and a simulated pi child all run to completion while it is held and send nothing.
// After release, every event was delivered exactly once with one ledger row each.
func TestWakeScan_ConcurrentTriggers_OneClaimPerRegistrationEvent(t *testing.T) {
	f := newScanFixture(t)
	seqs := []int64{f.addEvent("sender"), f.addEvent("sender"), f.addEvent("sender")}
	entered, release := make(chan int64, 1), make(chan struct{})
	var once sync.Once
	f.adapter.onDeliver = func(seq int64) {
		once.Do(func() {
			entered <- seq
			<-release
		})
	}
	first := make(chan error, 1)
	go func() { first <- f.scanner(always).Scan(t.Context()) }()
	require.Equal(t, seqs[0], <-entered, "the first trigger holds the claim on the lowest event")

	require.NoError(t, f.scanner(always).Scan(t.Context()), "CLI trigger while the claim is in flight")
	require.NoError(t, f.scanner(always).Scan(t.Context()), "dispatcher trigger while the claim is in flight")
	require.NoError(t, f.d.Pass(t.Context(), piKeys(f, seqs)), "simulated pi child while the claim is in flight")
	require.Equal(t, seqs[:1], f.adapter.seqs(), "nothing else may be sent while the lowest claim is in flight")

	close(release)
	require.NoError(t, <-first)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.NoError(t, f.d.Pass(t.Context(), piKeys(f, seqs)))
	require.Equal(t, seqs, f.adapter.seqs(), "each event is delivered exactly once, in order")
	require.Equal(t, len(seqs), f.ledgerRows(), "one ledger row per (registration, event)")
}

func piKeys(f *scanFixture, seqs []int64) []model.WakeKey {
	keys := make([]model.WakeKey, 0, len(seqs))
	for _, s := range seqs {
		k := f.key
		k.EventSeq = s
		keys = append(keys, k)
	}
	return keys
}

func TestWakeScan_ParallelTriggers_DeliverEachEventOnce(t *testing.T) {
	f := newScanFixture(t)
	seqs := []int64{f.addEvent("sender"), f.addEvent("sender"), f.addEvent("sender"), f.addEvent("sender")}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%4 == 3 {
				_ = f.d.Pass(t.Context(), piKeys(f, seqs))
				return
			}
			_ = f.scanner(always).Scan(t.Context())
		}(i)
	}
	close(start)
	wg.Wait()
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, seqs, f.adapter.seqs())
	require.Equal(t, len(seqs), f.ledgerRows())
}

func TestWakeScan_UnwritableOperatorState_ClaimsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	f := newScanFixture(t)
	seq := f.addEvent("sender")
	dir, err := f.state.Path()
	require.NoError(t, err)
	require.NoError(t, f.state.Ensure())
	require.NoError(t, os.Chmod(dir, 0o500))
	restored := false
	restore := func() {
		if !restored {
			require.NoError(t, os.Chmod(dir, 0o700))
			restored = true
		}
	}
	t.Cleanup(restore)

	sandboxed := f.scanner(f.state.ProbeWritable)
	require.NoError(t, sandboxed.Scan(t.Context()))
	require.Empty(t, f.adapter.seqs(), "a sandboxed trigger sends nothing")
	require.Zero(t, f.ledgerRows(), "a sandboxed trigger claims nothing")
	hw, _ := f.mark()
	require.Zero(t, hw)

	restore()
	require.NoError(t, f.scanner(f.state.ProbeWritable).Scan(t.Context()))
	require.Equal(t, []int64{seq}, f.adapter.seqs(), "the dispatcher wakes once")
	require.NoError(t, sandboxed.Scan(t.Context()))
	require.Equal(t, []int64{seq}, f.adapter.seqs())
}

func TestWakeScan_LiveWakeOffForAgent_NoSend(t *testing.T) {
	for _, scope := range []string{"agent", "host"} {
		t.Run(scope, func(t *testing.T) {
			f := newScanFixture(t)
			seq := f.addEvent("sender")
			agent := ""
			if scope == "agent" {
				agent = f.key.AgentID
			}
			_, err := wakeConfig(t, f.state, agent, true).Set("wake.live", "off")
			require.NoError(t, err)

			require.NoError(t, f.scanner(always).Scan(t.Context()))

			require.Empty(t, f.adapter.seqs())
			require.Zero(t, f.ledgerRows())
			hw, hold := f.mark()
			require.Zero(t, hw)
			require.False(t, hold)
			events, err := f.store.InboxList(t.Context(), f.key.AgentID)
			require.NoError(t, err)
			require.Len(t, events, 1)
			require.Equal(t, seq, events[0].Seq, "the event stays in the inbox")

			_, err = wakeConfig(t, f.state, agent, true).Set("wake.live", "on")
			require.NoError(t, err)
			require.NoError(t, f.scanner(always).Scan(t.Context()))
			require.Equal(t, []int64{seq}, f.adapter.seqs(), "woken once live wake is back on")
		})
	}
}

func TestWakeScan_SelfAddressed_Skipped(t *testing.T) {
	f := newScanFixture(t)
	own := f.addEvent(f.key.AgentID)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs(), "an event the agent authored never wakes it")
	require.Zero(t, f.ledgerRows())
	hw, _ := f.mark()
	require.Equal(t, own, hw, "the mark moves over the skipped event so it is not re-read")

	foreign := f.addEvent("sender")
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{foreign}, f.adapter.seqs(), "a following foreign event still wakes")
}

func TestWakeScan_SelfAddressedBelowHeldEvent_DoesNotStallRegistration(t *testing.T) {
	f := newScanFixture(t)
	own := f.addEvent(f.key.AgentID)
	held := f.addEvent("sender")
	f.wstore.hops[held] = 3 // over the hop limit of 1
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs())
	_, hold := f.mark()
	require.True(t, hold)

	f.wstore.hops[held] = 0
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{held}, f.adapter.seqs(), "own event %d must not block the held foreign event", own)
}

func TestWakeScan_DeadRoute_RowMarkedStale_EventStays(t *testing.T) {
	f := newScanFixture(t)
	f.addEvent("sender")
	f.liveness.state.Store(int32(service.LiveDead))
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs())
	require.Zero(t, f.ledgerRows())
	require.NotEmpty(t, f.staleSince(f.key.AgentID), "the dead row is marked stale")
	events, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	require.Len(t, events, 1, "the event stays in the inbox")
	hw, _ := f.mark()
	require.Zero(t, hw)
}

func TestWakeScan_UnknownLiveness_FailsSafeAsLive(t *testing.T) {
	f := newScanFixture(t)
	seq := f.addEvent("sender")
	f.liveness.state.Store(int32(service.LiveUnknown))
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{seq}, f.adapter.seqs())
	require.Empty(t, f.staleSince(f.key.AgentID))
}

func TestWakeScan_HoldLifted_WokenAtNextTrigger(t *testing.T) {
	f := newScanFixture(t)
	first, second := f.addEvent("sender"), f.addEvent("sender")
	f.wstore.hops[first] = 3
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs(), "the held event blocks the later one")
	hw, hold := f.mark()
	require.Zero(t, hw)
	require.True(t, hold)

	f.wstore.hops[first] = 0
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{first, second}, f.adapter.seqs(), "the held event goes first")
	hw, hold = f.mark()
	require.Equal(t, second, hw)
	require.False(t, hold)
}

func TestWakeScan_NoLiveRoute_ClaimsNothing_EventsStay(t *testing.T) {
	f := newScanFixture(t)
	seq := f.addEvent("sender")
	f.adapter.live.Store(false)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs())
	require.Zero(t, f.ledgerRows(), "no claim and no ledger row without a live route")
	hw, _ := f.mark()
	require.Zero(t, hw, "the mark stays so a later adapter still gets the event")

	f.adapter.live.Store(true)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{seq}, f.adapter.seqs())
}

func TestWakeScan_NotWakeable_PassesNothing(t *testing.T) {
	f := newScanFixture(t)
	seq := f.addEvent("sender")
	f.wakeable.Store(false)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs())
	require.Zero(t, f.ledgerRows())
	f.wakeable.Store(true)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{seq}, f.adapter.seqs())
}

func TestWakeScan_PiRegistration_Skipped(t *testing.T) {
	f := newScanFixture(t)
	_, err := f.store.WriteDB().ExecContext(t.Context(), "UPDATE agent_registrations SET harness='pi'")
	require.NoError(t, err)
	f.addEvent("sender")
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Empty(t, f.adapter.seqs(), "the pi wait child claims for pi rows")
	require.Zero(t, f.ledgerRows())
}

// A claim error on a lower event sets no hold. The later event must not overtake it,
// and the next trigger retries from the lowest (ADR-007 section 2.7).
func TestWakeScan_NonHeldBlockedEvent_NotOvertaken(t *testing.T) {
	f := newScanFixture(t)
	first, second := f.addEvent("sender"), f.addEvent("sender")
	f.wstore.failSeq, f.wstore.failOnce = first, true

	err := f.scanner(always).Scan(t.Context())
	require.ErrorIs(t, err, model.ErrConflict)
	require.Empty(t, f.adapter.seqs(), "the later event must not overtake the blocked one")
	hw, hold := f.mark()
	require.Zero(t, hw)
	require.False(t, hold)

	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{first, second}, f.adapter.seqs())
}

func TestWakeScan_OnePassPerScan_PassesEveryEventAboveMark(t *testing.T) {
	f := newScanFixture(t)
	seqs := []int64{f.addEvent("sender"), f.addEvent("sender"), f.addEvent("sender")}
	rec := &recordingRoutes{inner: f.d}
	opts := f.options(always)
	opts.Dispatcher = rec
	s, err := service.NewWakeScanner(opts)
	require.NoError(t, err)
	require.NoError(t, s.Scan(t.Context()))
	require.Len(t, rec.passes, 1)
	require.Equal(t, seqs, rec.passes[0], "the scanner never passes a subset")
}

func TestWakeScan_GateFilter_RejectsDeadAndUngranted(t *testing.T) {
	f := newScanFixture(t)
	s := f.scanner(always)
	key := f.key
	ok, err := s.Gate(t.Context(), key)
	require.NoError(t, err)
	require.True(t, ok)
	f.wakeable.Store(false)
	ok, err = s.Gate(t.Context(), key)
	require.NoError(t, err)
	require.False(t, ok)
	f.wakeable.Store(true)
	f.liveness.state.Store(int32(service.LiveDead))
	ok, err = s.Gate(t.Context(), key)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestWakeScan_ExpireStale_RunsOnEveryScan(t *testing.T) {
	f := newScanFixture(t)
	calls := 0
	opts := f.options(always)
	opts.ExpireStale = func(context.Context) error { calls++; return nil }
	s, err := service.NewWakeScanner(opts)
	require.NoError(t, err)
	require.NoError(t, s.Scan(t.Context()))
	require.NoError(t, s.Scan(t.Context()))
	require.Equal(t, 2, calls)
}

func TestNewWakeScanner_MissingCapabilities_Refused(t *testing.T) {
	f := newScanFixture(t)
	for name, mutate := range map[string]func(*service.WakeScannerOptions){
		"store":      func(o *service.WakeScannerOptions) { o.Store = nil },
		"dispatcher": func(o *service.WakeScannerOptions) { o.Dispatcher = nil },
		"liveness":   func(o *service.WakeScannerOptions) { o.Liveness = nil },
		"wakeable":   func(o *service.WakeScannerOptions) { o.Wakeable = nil },
		"writable":   func(o *service.WakeScannerOptions) { o.Writable = nil },
		"host":       func(o *service.WakeScannerOptions) { o.HostID = "" },
		"uid":        func(o *service.WakeScannerOptions) { o.UID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			opts := f.options(always)
			mutate(&opts)
			_, err := service.NewWakeScanner(opts)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

type recordingRoutes struct {
	inner  *service.WakeDispatcher
	passes [][]int64
}

func (r *recordingRoutes) PassSkipping(ctx context.Context, keys []model.WakeKey, skip map[model.WakeKey]bool) error {
	var seqs []int64
	for _, k := range keys {
		seqs = append(seqs, k.EventSeq)
	}
	r.passes = append(r.passes, seqs)
	return r.inner.PassSkipping(ctx, keys, skip)
}

func (r *recordingRoutes) HasLiveRoute(ctx context.Context, key model.WakeKey) bool {
	return r.inner.HasLiveRoute(ctx, key)
}

// Reevaluate after a grant or setting change meets the same gates as a scan.
func TestWakeScan_GateAsCandidateFilter_ReevaluateHonoursGates(t *testing.T) {
	f := newScanFixture(t)
	first := f.addEvent("sender")
	f.gate = f.scanner(always).Gate
	f.wstore.hops[first] = 3
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	_, hold := f.mark()
	require.True(t, hold)

	f.wstore.hops[first] = 0
	f.wakeable.Store(false)
	require.NoError(t, f.d.Reevaluate(t.Context(), f.key.AgentID))
	require.Empty(t, f.adapter.seqs(), "an ungranted agent is not woken by re-evaluation")

	f.wakeable.Store(true)
	require.NoError(t, f.d.Reevaluate(t.Context(), f.key.AgentID))
	require.Equal(t, []int64{first}, f.adapter.seqs())
}
