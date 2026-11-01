// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// defaultStatusRows is how many recent wake rows mtix inbox status shows.
const defaultStatusRows = 10

// wakeStatusHost resolves this host's identity read-only. An empty host id means no
// registration can exist yet (nothing has registered), which is a state, not an error.
func wakeStatusHost() (host, uid string, err error) {
	h, err := existingRegistrationDoctorHost()
	if err != nil {
		return "", "", err
	}
	return h.HostID, h.UID, nil
}

// wakeStatusSettings reads the effective wake.live and wake.confirm_window for an agent.
func wakeStatusSettings(agent string) (bool, time.Duration, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return false, 0, fmt.Errorf("session identity: %w", err)
	}
	values, reports, err := oplocal.New(env).WakeSettings(agent)
	if err != nil {
		return false, 0, fmt.Errorf("read wake settings: %w", err)
	}
	if len(reports) > 0 {
		return false, 0, fmt.Errorf("foreign host wake settings: %w", model.ErrOperatorStateUnreadable)
	}
	// Operator state is normalized on read (1..WakeConfirmWindowMaxSeconds), so a hand-edited
	// out-of-range value already fails above and can never overflow the duration here.
	seconds, err := strconv.ParseInt(values[model.WakeConfirmWindowKey].Value, 10, 64)
	if err != nil {
		return false, 0, fmt.Errorf("%s is not a whole number of seconds: %w", model.WakeConfirmWindowKey, model.ErrInvalidInput)
	}
	return values["wake.live"].Value != "false", time.Duration(seconds) * time.Second, nil
}

// newCLIWakeStatusService wires the status service to the open board (MTIX-112.40.6).
func newCLIWakeStatusService() *service.WakeStatusService {
	return service.NewWakeStatusService(service.WakeStatusDeps{
		Settings: wakeStatusSettings,
		Registered: func(ctx context.Context, agent string) (bool, error) {
			agents, err := registeredWakeAgents(ctx)
			if err != nil {
				return false, err
			}
			for _, a := range agents {
				if a == agent {
					return true, nil
				}
			}
			return false, nil
		},
		Dispatcher: dispatcherAlive,
		Rows: func(ctx context.Context, agent string, limit int) ([]model.WakeStatusRow, error) {
			host, uid, err := wakeStatusHost()
			if err != nil || host == "" {
				return nil, err
			}
			return app.store.WakeStatusRows(ctx, host, uid, agent, limit)
		},
		Clock: time.Now,
	})
}

// registeredWakeAgents lists the agent ids registered on this host and user.
func registeredWakeAgents(ctx context.Context) ([]string, error) {
	host, uid, err := wakeStatusHost()
	if err != nil || host == "" {
		return nil, err
	}
	regs, err := app.store.ListRegistrations(ctx, host, uid)
	if err != nil {
		return nil, fmt.Errorf("list registrations: %w", err)
	}
	agents := make([]string, 0, len(regs))
	for _, r := range regs {
		agents = append(agents, r.AgentID)
	}
	return agents, nil
}

// wakeStatusLine renders one status for text output.
func wakeStatusLine(s service.WakeStatus) string {
	line := string(s.State)
	if s.Route != "" {
		line += " via " + s.Route
	}
	if s.Reason != "" {
		line += " (" + s.Reason + ")"
	}
	return line
}

func newInboxStatusCmd() *cobra.Command {
	var (
		agent string
		limit int
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the wake delivery state for an agent and its recent wake outcomes",
		Long: `Show the wake delivery state for an agent and its last wake outcomes with age
(MTIX-112.40.6). States: queued, nudged, unconfirmed, confirmed, held, undeliverable,
off, no-live-session, no-dispatcher, unknown. unconfirmed is derived at read time from
wake.confirm_window (default 30 seconds) and is never written as an inbox event.
This reads local state only and never contacts a hub.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return runInboxStatus(agent, limit) },
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id whose wake status to show (required)")
	cmd.Flags().IntVar(&limit, "limit", defaultStatusRows, "Number of recent wake rows to show (1-1000)")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func runInboxStatus(agent string, limit int) error {
	if limit < 1 || limit > 1000 {
		return fmt.Errorf("inbox status: --limit must be 1..1000: %w", model.ErrInvalidInput)
	}
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	ctx := context.Background()
	svc := newCLIWakeStatusService()
	current := svc.Current(ctx, agent)
	recent, err := svc.Recent(ctx, agent, limit)
	if err != nil {
		// An unreadable source is shown as unknown with its cause, never an aborted listing.
		recent = []service.WakeStatus{svc.Unreadable(agent, err)}
	}
	if app.jsonOutput {
		data, err := json.Marshal(map[string]any{"agent": agent, "current": current, "recent": recent})
		if err != nil {
			return fmt.Errorf("encode inbox status: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "current: %s\n", wakeStatusLine(current))
	if current.Fix != "" {
		fmt.Fprintf(&b, "  fix: %s\n", current.Fix)
	}
	if len(recent) == 0 {
		b.WriteString("no wake rows recorded for this agent (nothing has been nudged yet)\n")
	}
	for _, r := range recent {
		fmt.Fprintf(&b, "event %d: %s, %ds ago\n", r.EventSeq, wakeStatusLine(r), r.AgeSeconds)
	}
	fmt.Print(b.String())
	return nil
}

// dispatcherAlive reports whether any dispatcher heartbeat exists for this host and user.
func dispatcherAlive(ctx context.Context) (bool, error) {
	host, uid, err := wakeStatusHost()
	if err != nil || host == "" {
		return false, err
	}
	beats, err := app.store.ListDispatcherHeartbeats(ctx, host, uid)
	if err != nil {
		return false, fmt.Errorf("list dispatcher heartbeats: %w", err)
	}
	return len(beats) > 0, nil
}
