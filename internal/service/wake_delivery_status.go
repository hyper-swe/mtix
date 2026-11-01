// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeStatus is the sender-visible wake state of an addressed message or a ledger row
// (MTIX-112.40.6, design note section 4). The inbox is the source of truth: a state other
// than confirmed means the message is in the inbox and the wake is not confirmed.
type WakeStatus struct {
	State      model.WakeState `json:"state"`
	Route      string          `json:"route,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Since      string          `json:"since,omitempty"`
	EventSeq   int64           `json:"event_seq,omitempty"`
	AgeSeconds int64           `json:"age_seconds,omitempty"`
	Fix        string          `json:"fix,omitempty"`
}

// WakeStatusDeps are the injected readers behind WakeStatusService. Every reader is
// read-only; an error from any of them yields state unknown with the cause, never a skip.
type WakeStatusDeps struct {
	// Settings returns the effective wake.live and wake.confirm_window for the agent.
	Settings func(agent string) (live bool, window time.Duration, err error)
	// Registered reports whether the agent has a registration on this host.
	Registered func(ctx context.Context, agent string) (bool, error)
	// Dispatcher reports whether any dispatcher heartbeat exists for this host and user.
	Dispatcher func(ctx context.Context) (bool, error)
	// Rows lists the newest retained wake ledger rows for the agent.
	Rows  func(ctx context.Context, agent string, limit int) ([]model.WakeStatusRow, error)
	Clock func() time.Time
}

// WakeStatusService derives delivery status without writing anything: unconfirmed is
// computed at read time and is never recorded as an inbox event (lead ruling R3).
type WakeStatusService struct{ deps WakeStatusDeps }

// NewWakeStatusService builds the service from its readers.
func NewWakeStatusService(deps WakeStatusDeps) *WakeStatusService {
	return &WakeStatusService{deps: deps}
}

// Current reports what is knowable when a message is addressed to the agent: off,
// no-live-session, no-dispatcher or queued. It never returns an error: a failed reader
// yields unknown with the cause, so the failure is shown to the sender (FR-20).
func (s *WakeStatusService) Current(ctx context.Context, agent string) WakeStatus {
	live, _, err := s.deps.Settings(agent)
	if err != nil {
		return unknownStatus(agent, "settings: "+err.Error())
	}
	if !live {
		return newWakeStatus(model.WakeStateOff, agent)
	}
	registered, err := s.deps.Registered(ctx, agent)
	if err != nil {
		return unknownStatus(agent, "registration: "+err.Error())
	}
	if !registered {
		return newWakeStatus(model.WakeStateNoLiveSession, agent)
	}
	dispatching, err := s.deps.Dispatcher(ctx)
	if err != nil {
		return unknownStatus(agent, "dispatcher: "+err.Error())
	}
	if !dispatching {
		return newWakeStatus(model.WakeStateNoDispatcher, agent)
	}
	return newWakeStatus(model.WakeStateQueued, agent)
}

// Wake status sources named by WakeSourceError.
const (
	WakeSourceSettings = "settings"
	WakeSourceLedger   = "ledger"
)

// WakeSourceError says which source a status read failed on, so a caller can point at a
// fix that does not depend on that same source.
type WakeSourceError struct {
	Source string
	Err    error
}

func (e *WakeSourceError) Error() string { return "read wake " + e.Source + ": " + e.Err.Error() }

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *WakeSourceError) Unwrap() error { return e.Err }

// Unreadable reports a failed status read as state unknown with the cause, so a listing
// shows the failure instead of aborting (FR-20: failures are never silent).
func (s *WakeStatusService) Unreadable(agent string, cause error) WakeStatus {
	return unknownStatus(agent, cause.Error())
}

// Recent maps the newest retained ledger rows to delivery states, newest first.
func (s *WakeStatusService) Recent(ctx context.Context, agent string, limit int) ([]WakeStatus, error) {
	_, window, err := s.deps.Settings(agent)
	if err != nil {
		return nil, &WakeSourceError{Source: WakeSourceSettings, Err: err}
	}
	rows, err := s.deps.Rows(ctx, agent, limit)
	if err != nil {
		return nil, &WakeSourceError{Source: WakeSourceLedger, Err: err}
	}
	now := s.deps.Clock()
	out := make([]WakeStatus, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowStatus(r, agent, now, window))
	}
	return out, nil
}

// rowStatus maps one ledger row. A time that cannot be read for a sent nudge is unknown,
// because the confirmation window cannot be judged (never silently nudged).
func rowStatus(r model.WakeStatusRow, agent string, now time.Time, window time.Duration) WakeStatus {
	since := r.OutcomeAt
	if since == "" {
		since = r.ClaimedAt
	}
	st := WakeStatus{Route: r.Route, Reason: r.Reason, Since: since, EventSeq: r.EventSeq}
	at, err := time.Parse(time.RFC3339Nano, since)
	if err != nil && r.Outcome.Canonical() == model.WakeSent {
		st.State, st.Reason = model.WakeStateUnknown, "time unreadable: "+since
		st.Fix = statusFix(st.State, agent)
		return st
	}
	age := now.Sub(at)
	if err == nil && age > 0 {
		st.AgeSeconds = int64(age / time.Second)
	}
	st.State = r.Outcome.State(age, window)
	if st.State == model.WakeStateUnknown && st.Reason == "" {
		st.Reason = "unrecognized outcome " + string(r.Outcome)
	}
	if st.State == model.WakeStateHeld && st.Reason == "" {
		st.Reason = "approving a held wake does not yet send a fresh nudge (MTIX-112.40.10); read the inbox"
	}
	st.Fix = statusFix(st.State, agent)
	return st
}

func newWakeStatus(state model.WakeState, agent string) WakeStatus {
	return WakeStatus{State: state, Fix: statusFix(state, agent)}
}

func unknownStatus(agent, reason string) WakeStatus {
	return WakeStatus{State: model.WakeStateUnknown, Reason: reason, Fix: statusFix(model.WakeStateUnknown, agent)}
}

// statusFix names a runnable next step that exists in this build for each state.
func statusFix(state model.WakeState, agent string) string {
	switch state {
	case model.WakeStateOff:
		return "mtix config set wake.live on --agent " + agent
	case model.WakeStateNoLiveSession:
		return "mtix agent register " + agent
	case model.WakeStateNoDispatcher, model.WakeStateUnconfirmed, model.WakeStateUndeliverable:
		return "mtix inbox --agent " + agent + " --wait"
	case model.WakeStateHeld:
		return "mtix agent allow " + agent
	case model.WakeStateQueued, model.WakeStateNudged:
		return "mtix inbox status --agent " + agent
	case model.WakeStateUnknown:
		return "mtix doctor --json"
	}
	return ""
}
