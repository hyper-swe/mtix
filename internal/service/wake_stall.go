// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// StallInput is what the stall rule judges for one addressed, unacknowledged event.
type StallInput struct {
	Now    time.Time
	Window time.Duration // T = wake.confirm_window
	// HasRow says a wake ledger row exists; Outcome and RowAt are that row's outcome and the
	// time it was recorded. EventAt is when the event was created, used when there is no row.
	HasRow     bool
	Outcome    model.WakeOutcome
	RowAt      time.Time
	EventAt    time.Time
	WaiterLive bool
}

// EventStalled is the stall rule (MTIX-112.40.12, FR-20). An addressed, unacknowledged event
// is stalled when no hook waiter is armed and more than T = wake.confirm_window has passed
// since the event (no row) or since its push was recorded (sent row). It is derived from
// local state only: no timer runs, nothing is written, and a non-positive window never
// stalls. A stall is noticed at the next dispatcher pass, sender status read or doctor run.
func EventStalled(in StallInput) bool {
	if in.Window <= 0 || in.WaiterLive {
		return false
	}
	at := in.EventAt
	if in.HasRow {
		if in.Outcome.Canonical() != model.WakeSent {
			return false
		}
		at = in.RowAt
	}
	return in.Now.Sub(at) > in.Window
}

// WakeWaiterReader reads whether a hook waiter is armed for a registration.
type WakeWaiterReader interface {
	WakeWaiterLive(ctx context.Context, key WakeLeaseKey) (bool, error)
}

// WakeStallDeps feed stalled-turn recovery in the dispatcher (MTIX-112.40.12). When nil the
// dispatcher derives them from its settings reader and store, and recovery is off when
// either cannot supply them.
type WakeStallDeps struct {
	// Window returns T = wake.confirm_window for the agent.
	Window func(agent string) (time.Duration, error)
	// WaiterLive reports whether a hook waiter is armed for the registration.
	WaiterLive func(ctx context.Context, key model.WakeKey) (bool, error)
	// HookRoute reports whether the key's registration wakes through the hook route, the only
	// route that can stall. Nil means every registration; the derived default reads the
	// registration, so a legacy or unregistered wake is never redelivered as a stall.
	HookRoute func(ctx context.Context, key model.WakeKey) (bool, error)
}

// defaultStallDeps derives recovery inputs from the dispatcher's settings and store.
func defaultStallDeps(opts WakeDispatcherOptions) *WakeStallDeps {
	if opts.Stall != nil {
		return opts.Stall
	}
	waiters, ok := opts.Store.(WakeWaiterReader)
	if !ok || opts.Settings == nil {
		return nil
	}
	return &WakeStallDeps{
		Window: func(agent string) (time.Duration, error) { return confirmWindow(opts.Settings, agent) },
		WaiterLive: func(ctx context.Context, key model.WakeKey) (bool, error) {
			return waiters.WakeWaiterLive(ctx, WakeLeaseKey{AgentID: key.AgentID, HostID: key.HostID, UID: key.UID})
		},
		HookRoute: func(ctx context.Context, key model.WakeKey) (bool, error) {
			return hookRegistration(ctx, opts.Store, key)
		},
	}
}

// IsHookRouteHarness reports whether a registration harness wakes through the hook route,
// the only route that arms a waiter and so the only one that can stall. It is the one place
// the stalled report and the recovery decide this; the value is the harness name real
// registrations store (MTIX-112.40.12).
func IsHookRouteHarness(harness string) bool { return harness == procinfo.HarnessClaude }

// hookRegistration reports whether the key's agent has a hook-route registration on its
// host and user: the hook route. A store that cannot list registrations has none.
func hookRegistration(ctx context.Context, store WakeStore, key model.WakeKey) (bool, error) {
	lister, ok := store.(interface {
		ListRegistrations(ctx context.Context, hostID, uid string) ([]sqlite.Registration, error)
	})
	if !ok {
		return false, nil
	}
	regs, err := lister.ListRegistrations(ctx, key.HostID, key.UID)
	if err != nil {
		return false, fmt.Errorf("list registrations for stall recovery: %w", err)
	}
	for _, r := range regs {
		if r.AgentID == key.AgentID {
			return IsHookRouteHarness(r.Harness), nil
		}
	}
	return false, nil
}

// confirmWindow reads the effective wake.confirm_window for an agent.
func confirmWindow(settings WakeSettingsReader, agent string) (time.Duration, error) {
	values, reports, err := settings.WakeSettings(agent)
	if err != nil {
		return 0, fmt.Errorf("read wake confirm window: %w", err)
	}
	if len(reports) > 0 {
		return 0, fmt.Errorf("foreign host wake settings: %w", model.ErrOperatorStateUnreadable)
	}
	var seconds int64
	if _, err := fmt.Sscanf(values[model.WakeConfirmWindowKey].Value, "%d", &seconds); err != nil || seconds <= 0 {
		return 0, fmt.Errorf("%s must be a positive whole number of seconds: %w", model.WakeConfirmWindowKey, model.ErrInvalidInput)
	}
	return time.Duration(seconds) * time.Second, nil
}

// stalledPassLimit bounds how many stalled candidates one pass examines.
const stalledPassLimit = 16

// stalledPassPages bounds how many candidate pages one pass reads.
const stalledPassPages = 8

// WakeStalledLister reads the sent, still unacknowledged wake rows a stalled turn may have
// left unconfirmed (MTIX-112.40.12).
type WakeStalledLister interface {
	StalledWakeCandidates(ctx context.Context, q sqlite.StalledWakeQuery) ([]model.WakeKey, error)
}

// pushRoutePresent reports whether any adapter other than the hook waiter and inbox-only
// route is registered: only such a route can redeliver a stalled event. Without one the
// pass reads nothing, so an idle host pays no scan (scale-to-zero friendly).
func (d *WakeDispatcher) pushRoutePresent() bool {
	for _, a := range d.adapters {
		if r := a.Route(); r != RouteHookWaiter && r != RouteInboxOnly {
			return true
		}
	}
	return false
}

// recoverStalled redelivers, at most once per event, the events whose push went unconfirmed
// because the session's turn never ended (MTIX-112.40.12). It does not depend on the keys the
// scanner passes: those sit above each registration's mark, and a sent event is below it.
// It selects the candidates itself from the wake ledger, bounded per pass in pages of
// stalledPassLimit that advance past refused rows, so recovery is driven by the activity
// that runs the pass and by no timer. Known limit: one pass examines at most
// stalledPassPages*stalledPassLimit (128) candidates, so more than 128 hook-route rows that the
// per-agent gates refuse (wake.live off, budget, hop guard, live waiter) can delay a real
// stall until those rows settle or leave the set. With the hook route alone nothing is read or written,
// so a later Stop waiter still claims the event. A failure on one event does not stop the others.
func (d *WakeDispatcher) recoverStalled(ctx context.Context) error {
	lister, ok := d.store.(WakeStalledLister)
	if d.stall == nil || d.claimer == nil || !ok || !d.pushRoutePresent() {
		return nil
	}
	q := sqlite.StalledWakeQuery{HostID: d.reeval.host, UID: d.reeval.uid, Limit: stalledPassLimit}
	if d.stall.HookRoute != nil {
		q.Harness = procinfo.HarnessClaude // only hook-route registrations can stall
	}
	var failures error
	for page := 0; page < stalledPassPages; page++ {
		keys, err := lister.StalledWakeCandidates(ctx, q)
		if err != nil {
			return errors.Join(failures, fmt.Errorf("select stalled wake candidates: %w", err))
		}
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return errors.Join(failures, fmt.Errorf("continue stalled recovery: %w", err))
			}
			failures = errors.Join(failures, d.recoverStalledOne(ctx, key))
		}
		if len(keys) < stalledPassLimit {
			break
		}
		last := keys[len(keys)-1]
		q.AfterSeq, q.AfterAgent = last.EventSeq, last.AgentID
	}
	return failures
}

// recoverStalledOne runs the same gates a first wake runs: a live push route, the live wake
// switch, the hop guard and the budget and back-off admission. Nothing it refuses is
// written. The wake.live switch off means no claim and no adapter call (FR-20.3).
func (d *WakeDispatcher) recoverStalledOne(ctx context.Context, key model.WakeKey) error {
	if !d.HasLiveRoute(ctx, key) {
		return nil // hook route alone: derived report only, no ledger write
	}
	if off, err := d.liveWakeOff(key.AgentID); err != nil || off {
		return err
	}
	window, err := d.stall.Window(key.AgentID)
	if err != nil || window <= 0 {
		return err
	}
	if live, lerr := d.stall.WaiterLive(ctx, key); lerr != nil || live {
		return lerr
	}
	if d.stall.HookRoute != nil {
		if hook, herr := d.stall.HookRoute(ctx, key); herr != nil || !hook {
			return herr
		}
	}
	hop, verdict, err := d.hopVerdict(ctx, key)
	if err != nil || verdict != verdictSend {
		return err
	}
	attempt, won, err := d.claimer.ReclaimStalled(ctx, key, window, d.budgets.admitStalled)
	if err != nil || !won {
		return err
	}
	attempt.Hop = hop
	_, err = d.deliverClaimed(ctx, attempt)
	return err
}
