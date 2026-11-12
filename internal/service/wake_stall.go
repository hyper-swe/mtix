// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
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
	}
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

// recoverStalled redelivers, at most once per event, the events of the passed keys whose push
// went unconfirmed because the session's turn never ended (MTIX-112.40.12). It runs at the
// start of a pass, only when a push route is live for the registration: with the hook route
// alone nothing is written, so a later Stop waiter still claims the event. Every redelivery
// goes through the single WakeClaimer, then the same deliverWake and recordWake as a normal
// send. A failure on one event does not stop the others.
func (d *WakeDispatcher) recoverStalled(ctx context.Context, keys []model.WakeKey) error {
	if d.stall == nil || d.claimer == nil {
		return nil
	}
	var failures error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, fmt.Errorf("continue stalled recovery: %w", err))
		}
		failures = errors.Join(failures, d.recoverStalledOne(ctx, key))
	}
	return failures
}

func (d *WakeDispatcher) recoverStalledOne(ctx context.Context, key model.WakeKey) error {
	if !d.HasLiveRoute(ctx, key) {
		return nil // hook route alone: derived report only, no ledger write
	}
	window, err := d.stall.Window(key.AgentID)
	if err != nil || window <= 0 {
		return err
	}
	live, err := d.stall.WaiterLive(ctx, key)
	if err != nil || live {
		return err
	}
	attempt, won, err := d.claimer.ReclaimStalled(ctx, key, window)
	if err != nil || !won {
		return err
	}
	result, sendErr := d.deliverWake(ctx, attempt)
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	recordErr := d.recordWake(finishCtx, attempt, result)
	if sendErr != nil {
		sendErr = fmt.Errorf("redeliver stalled wake: %w", sendErr)
	}
	if recordErr != nil {
		recordErr = fmt.Errorf("record stalled wake redelivery: %w", recordErr)
	}
	return errors.Join(sendErr, recordErr)
}
