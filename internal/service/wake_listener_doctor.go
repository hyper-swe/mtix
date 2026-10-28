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

const (
	listenerRateWindow  = 24 * time.Hour
	listenerRateMinimum = 5  // fewer nudges than this are too few to judge
	listenerRatePercent = 20 // unconfirmed share above this warns; compared by cross-multiplication, never by truncating division
)

// WakeListenerDoctorDeps feed the wake.listener doctor rows (MTIX-112.40.6).
type WakeListenerDoctorDeps struct {
	// Agents lists the agent ids registered on this host.
	Agents func(ctx context.Context) ([]string, error)
	// Dispatcher reports whether any dispatcher heartbeat exists for this host and user.
	Dispatcher func(ctx context.Context) (bool, error)
	Status     *WakeStatusService
	Clock      func() time.Time
}

// unbuiltListenerRoute is a delivery route or check whose adapter is not in this build.
type unbuiltListenerRoute struct{ id, route, blockedBy, what string }

func unbuiltListenerRoutes() []unbuiltListenerRoute {
	return []unbuiltListenerRoute{
		{"wake.listener.hook-installed", "hook-waiter", "MTIX-112.40.5", "the listener hook install check"},
		{"wake.listener.waiter-armed", "hook-waiter", "MTIX-112.40.4", "the armed-waiter check"},
		{"wake.listener.route-contract", "claude-socket", "MTIX-112.40.3", "the session-socket contract probe"},
		{"wake.listener.bypass-hold", "claude-socket", "MTIX-112.40.3", "the bypass-mode hold check"},
	}
}

// WakeListenerDoctorChecks returns every documented wake.listener row. Checks whose route
// adapter is not built are still returned, as INFO rows with data.reachable=false and
// data.blocked_by, so a dropped check is recorded explicitly and never a silent no-op.
func WakeListenerDoctorChecks(deps WakeListenerDoctorDeps) []DoctorCheck {
	checks := []DoctorCheck{
		{ID: "wake.listener.routes", Run: func(ctx context.Context) DoctorResult { return listenerRoutesRow(ctx, deps) }},
		{ID: "wake.listener.dispatcher", Run: func(ctx context.Context) DoctorResult { return listenerDispatcherRow(ctx, deps) }},
		{ID: "wake.listener.unconfirmed-rate", Run: func(ctx context.Context) DoctorResult { return listenerRateRow(ctx, deps) }},
	}
	for _, u := range unbuiltListenerRoutes() {
		checks = append(checks, DoctorCheck{ID: u.id, Run: func(ctx context.Context) DoctorResult { return listenerUnbuiltRow(ctx, deps, u) }})
	}
	return checks
}

// listenerAgentFix returns the interim runnable fix: the foreground wait for the first
// registered agent. With no registered agent there is no id to put in a command, so the fix
// is the command that lists registrations and the detail says how to register (see
// listenerNoAgentNote); a fix never carries a placeholder.
func listenerAgentFix(ctx context.Context, deps WakeListenerDoctorDeps) (fix string, registered bool) {
	if agents, err := deps.Agents(ctx); err == nil && len(agents) > 0 {
		return "mtix inbox --agent " + agents[0] + " --wait", true
	}
	return "mtix agent list --registrations", false
}

// listenerNoAgentNote is appended to a detail whose fix could not name an agent.
const listenerNoAgentNote = " No agent is registered: run mtix agent register with your agent id, then mtix inbox --agent with that id and --wait."

// listenerFailureFix points at a command that does not read the failing source.
func listenerFailureFix(err error) string {
	var src *WakeSourceError
	if errors.As(err, &src) && src.Source == WakeSourceSettings {
		return "mtix hooks status --json"
	}
	return "mtix doctor --json"
}

func listenerRoutesRow(ctx context.Context, deps WakeListenerDoctorDeps) DoctorResult {
	routes := []map[string]any{
		{"route": "hook-waiter", "reachable": false, "blocked_by": "MTIX-112.40.4"},
		{"route": "claude-socket", "reachable": false, "blocked_by": "MTIX-112.40.3"},
		{"route": "codex-queue", "reachable": false, "blocked_by": "MTIX-112.40.7"},
		{"route": "inbox-only", "reachable": true},
	}
	fix, registered := listenerAgentFix(ctx, deps)
	detail := "Delivery routes in this build: inbox-only (the message waits in the inbox until the agent reads it or runs a foreground wait). " +
		"Not reachable in this build: hook-waiter (MTIX-112.40.4), claude-socket (MTIX-112.40.3), codex-queue (MTIX-112.40.7)."
	if !registered {
		detail += listenerNoAgentNote
	}
	return DoctorResult{Status: DoctorInfo, Fix: fix, Data: map[string]any{"routes": routes}, Detail: detail}
}

func listenerDispatcherRow(ctx context.Context, deps WakeListenerDoctorDeps) DoctorResult {
	fix, _ := listenerAgentFix(ctx, deps)
	agents, err := deps.Agents(ctx)
	if err != nil {
		return DoctorResult{Status: DoctorFail, Detail: "Cannot list registered agents: " + err.Error(), Fix: "mtix doctor --json"}
	}
	if len(agents) == 0 {
		return DoctorResult{Status: DoctorInfo, Detail: "No registered agents on this host, so nothing needs a dispatcher." + listenerNoAgentNote, Fix: fix}
	}
	alive, err := deps.Dispatcher(ctx)
	if err != nil {
		return DoctorResult{Status: DoctorFail, Detail: "Cannot read dispatcher heartbeats: " + err.Error(), Fix: "mtix doctor --json"}
	}
	if !alive {
		return DoctorResult{Status: DoctorWarn, Fix: fix, Data: map[string]any{"agents": agents},
			Detail: fmt.Sprintf("%d registered agent(s) and no dispatcher heartbeat: addressed messages wait in the inbox and nothing nudges the session.", len(agents))}
	}
	return DoctorResult{Status: DoctorInfo, Fix: fix, Detail: "A dispatcher heartbeat exists for this host and user."}
}

func listenerRateRow(ctx context.Context, deps WakeListenerDoctorDeps) DoctorResult {
	agents, err := deps.Agents(ctx)
	if err != nil {
		return DoctorResult{Status: DoctorFail, Detail: "Cannot list registered agents: " + err.Error(), Fix: "mtix doctor --json"}
	}
	cutoff := deps.Clock().Add(-listenerRateWindow)
	counts := map[model.WakeState]int{}
	worst, worstCount := "", 0 // the agent with the most unconfirmed nudges; the first wins a tie
	for _, agent := range agents {
		recent, err := deps.Status.Recent(ctx, agent, 1000)
		if err != nil {
			return DoctorResult{Status: DoctorFail, Detail: "Cannot read wake status for " + agent + ": " + err.Error(), Fix: listenerFailureFix(err)}
		}
		agentUnconfirmed := 0
		for _, r := range recent {
			if at, perr := time.Parse(time.RFC3339Nano, r.Since); perr != nil || at.Before(cutoff) {
				continue
			}
			counts[r.State]++
			if r.State == model.WakeStateUnconfirmed {
				agentUnconfirmed++
			}
		}
		if agentUnconfirmed > worstCount {
			worst, worstCount = agent, agentUnconfirmed
		}
	}
	total := counts[model.WakeStateNudged] + counts[model.WakeStateUnconfirmed] + counts[model.WakeStateConfirmed]
	data := map[string]any{"nudged": counts[model.WakeStateNudged], "unconfirmed": counts[model.WakeStateUnconfirmed], "confirmed": counts[model.WakeStateConfirmed], "window_hours": 24}
	if total == 0 {
		return DoctorResult{Status: DoctorInfo, Detail: "No nudges were recorded in the last 24 hours.", Data: data, Fix: "mtix doctor --json"}
	}
	unconfirmed := counts[model.WakeStateUnconfirmed]
	detail := fmt.Sprintf("%d of %d nudges in the last 24 hours were not confirmed.", unconfirmed, total)
	if total >= listenerRateMinimum && unconfirmed*100 > listenerRatePercent*total {
		fix := "mtix inbox status --agent " + worst + " --json; mtix config set wake.confirm_window 60 --agent " + worst
		return DoctorResult{Status: DoctorWarn, Detail: detail, Data: data, Fix: fix}
	}
	return DoctorResult{Status: DoctorInfo, Detail: detail, Data: data, Fix: "mtix doctor --json"}
}

func listenerUnbuiltRow(ctx context.Context, deps WakeListenerDoctorDeps, u unbuiltListenerRoute) DoctorResult {
	fix, registered := listenerAgentFix(ctx, deps)
	detail := fmt.Sprintf("%s is not reachable in this build (route %s, %s not built yet); messages wait in the inbox.", u.what, u.route, u.blockedBy)
	if !registered {
		detail += listenerNoAgentNote
	}
	return DoctorResult{Status: DoctorInfo, Fix: fix, Detail: detail,
		Data: map[string]any{"reachable": false, "blocked_by": u.blockedBy, "route": u.route}}
}
