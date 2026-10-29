// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeRoute names a delivery route. The order of the constants is the selection order.
type WakeRoute string

const (
	// RouteHookWaiter is the documented hook route: a waiter armed by session hooks.
	RouteHookWaiter WakeRoute = "hook-waiter"
	// RouteSocketPush is the session-socket fast path.
	RouteSocketPush WakeRoute = "claude-socket"
	// RouteQueue is the Codex queue route, gated by ADR-007 D12.
	RouteQueue WakeRoute = "codex-queue"
	// RouteLegacy wraps the dispatcher's original Send hook.
	RouteLegacy WakeRoute = "legacy"
	// RouteInboxOnly is the terminal fallback: the event waits in the inbox.
	RouteInboxOnly WakeRoute = "inbox-only"
)

// reasonNoLiveRoute is the undeliverable reason when no adapter is eligible.
const reasonNoLiveRoute = "no-live-route"

// WakeCapabilities says what a route can do for one registration.
type WakeCapabilities struct {
	Live       bool // the route can reach the registration right now
	BypassSafe bool // the route is not subject to the bypass-mode peer hold
	Confirms   bool // the route can later report the nudge was seen
}

// WakeHealth is a cheap liveness answer used for selection and by doctor.
type WakeHealth struct {
	Healthy bool
	Detail  string
}

// ContractCheck reports whether a route's pinned external contract still holds (ADR-007 D9).
type ContractCheck struct {
	Pinned  string
	Matches bool
	Detail  string
}

// WakeAdapter is one delivery route for a claimed wake (MTIX-112.40 design note
// section 4; ADR-007 sections 2.1 and 6.3). Adapters never claim: the dispatcher
// claims once per (registration, event) before any Deliver runs. Deliver sends one
// nudge, never the message body (nudge-then-pull), and must not start timers.
type WakeAdapter interface {
	Route() WakeRoute
	Capabilities(key model.WakeKey) WakeCapabilities
	Deliver(ctx context.Context, attempt model.WakeAttempt) (model.WakeResult, error)
	Probe(ctx context.Context, key model.WakeKey) WakeHealth
	Contract(ctx context.Context) ContractCheck
}

func routeRank(r WakeRoute) int {
	switch r {
	case RouteHookWaiter:
		return 0
	case RouteSocketPush:
		return 1
	case RouteQueue:
		return 2
	case RouteLegacy:
		return 3
	}
	return 4
}

// orderAdapters returns adapters in fixed selection order; equal ranks keep input order.
func orderAdapters(in []WakeAdapter) []WakeAdapter {
	out := append([]WakeAdapter(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return routeRank(out[i].Route()) < routeRank(out[j].Route()) })
	return out
}

// legacyAdapter exposes the original Send hook as the last real route.
type legacyAdapter struct {
	send func(context.Context, model.WakeAttempt) error
}

func (legacyAdapter) Route() WakeRoute { return RouteLegacy }
func (legacyAdapter) Capabilities(model.WakeKey) WakeCapabilities {
	return WakeCapabilities{Live: true}
}
func (legacyAdapter) Probe(context.Context, model.WakeKey) WakeHealth {
	return WakeHealth{Healthy: true}
}
func (legacyAdapter) Contract(context.Context) ContractCheck {
	return ContractCheck{Pinned: "send-hook", Matches: true}
}

// Deliver returns a bare legacy outcome so the ledger row stays as earlier releases wrote it.
func (a legacyAdapter) Deliver(ctx context.Context, attempt model.WakeAttempt) (model.WakeResult, error) {
	if err := a.send(ctx, attempt); err != nil {
		return model.WakeResult{Outcome: model.WakeError}, err
	}
	return model.WakeResult{Outcome: model.WakeDelivered}, nil
}

// selectAdapter picks the first adapter in route order that is live and healthy for
// the registration; nil means inbox-only (FR-20, MTIX-112.40 route selection).
func (d *WakeDispatcher) selectAdapter(ctx context.Context, key model.WakeKey) WakeAdapter {
	for _, a := range d.adapters {
		if a.Capabilities(key).Live && a.Probe(ctx, key).Healthy {
			return a
		}
	}
	return nil
}

// HasLiveRoute reports whether a live, healthy adapter exists for the registration. The
// scanner claims nothing when it is false, so an event is not consumed before an adapter
// that can deliver it is registered (ADR-007 section 2.7 step 4, MTIX-112.17.8).
func (d *WakeDispatcher) HasLiveRoute(ctx context.Context, key model.WakeKey) bool {
	return d.selectAdapter(ctx, key) != nil
}

// deliverWake runs the selected route once. The claim is already won, so exactly one
// Deliver runs per (registration, event) however many routes are armed. A malformed
// adapter result is normalised to undeliverable/invalid-adapter-outcome so the finalize
// always succeeds and no claim is left reclaimable after Deliver ran.
func (d *WakeDispatcher) deliverWake(ctx context.Context, attempt model.WakeAttempt) (model.WakeResult, error) {
	a := d.selectAdapter(ctx, attempt.Key)
	if a == nil {
		return model.WakeResult{Outcome: model.WakeUndeliverable, Route: string(RouteInboxOnly), Reason: reasonNoLiveRoute}, nil
	}
	legacy := a.Route() == RouteLegacy
	res, err := a.Deliver(ctx, attempt)
	if err != nil {
		if legacy {
			return model.WakeResult{Outcome: model.WakeError}, err // keeps the original ledger name
		}
		return model.WakeResult{Outcome: model.WakeUndeliverable, Route: string(a.Route()), Reason: "deliver-error"}, err
	}
	if !validAdapterResult(res, legacy) {
		return model.WakeResult{Outcome: model.WakeUndeliverable, Route: string(a.Route()), Reason: "invalid-adapter-outcome"},
			fmt.Errorf("adapter %s returned unusable result %q: %w", a.Route(), res.Outcome, model.ErrInvalidInput)
	}
	if res.Route == "" && !legacy {
		res.Route = string(a.Route())
	}
	return res, nil
}

// validAdapterResult accepts the delivery outcomes an adapter may report. Only the legacy
// hook may return the old delivered and error names; confirmed belongs to the hook stamp.
func validAdapterResult(r model.WakeResult, legacy bool) bool {
	switch r.Outcome {
	case model.WakeSent, model.WakeHeld, model.WakeOff:
		return true
	case model.WakeUndeliverable:
		return r.Reason != ""
	case model.WakeDelivered, model.WakeError:
		return legacy
	}
	return false
}

// liveWakeOff reports whether the live wake switch is off for the agent (FR-20.3). It
// reads the same settings as the budget policy and re-evaluation (the injected reader,
// else the operator state). Off means no claim and no adapter call.
func (d *WakeDispatcher) liveWakeOff(agent string) (bool, error) {
	values, reports, err := d.budgets.settings.WakeSettings(agent)
	if err != nil {
		return false, fmt.Errorf("read live wake switch: %w", err)
	}
	if len(reports) > 0 {
		return false, fmt.Errorf("foreign host wake settings: %w", model.ErrOperatorStateUnreadable)
	}
	return values["wake.live"].Value == "false", nil
}

// WakeResultStore persists adapter outcomes; stores without it keep the two legacy outcomes.
type WakeResultStore interface {
	RecordWakeResult(context.Context, model.WakeAttempt, model.WakeResult) error
}
