// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Session registration commands: register output, unregister and list --registrations.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// sessionReport is what `agent register` tells the caller about the session.
type sessionReport struct {
	Outcome        string
	Harness        string
	Origin         string
	Epoch          int64
	NoticeSent     bool
	Warning        string
	RoutingWarning string
}

func (r *sessionReport) addTo(out map[string]any) {
	if r == nil {
		return
	}
	if r.Warning != "" {
		out["session_warning"] = r.Warning
		return
	}
	out["session"], out["harness"], out["origin"] = r.Outcome, r.Harness, r.Origin
	out["epoch"], out["notice_sent"] = r.Epoch, r.NoticeSent
	if r.RoutingWarning != "" {
		out["routing_warning"] = r.RoutingWarning
	}
}

func (r *sessionReport) print() {
	switch {
	case r == nil:
	case r.Warning != "":
		fmt.Fprintf(os.Stderr, "warning: session not registered: %s\n", r.Warning)
	default:
		fmt.Printf("Session %s (harness %s, origin %s, epoch %d)\n", strings.ReplaceAll(r.Outcome, "_", " "), r.Harness, r.Origin, r.Epoch)
		if r.RoutingWarning != "" {
			fmt.Fprintf(os.Stderr, "warning: session registered; checkout routing unavailable: %s; inspect mtix doctor\n", r.RoutingWarning)
		}
		if r.NoticeSent {
			fmt.Println("The displaced session was told with one notice.")
		}
	}
}

// registerCurrentSession records this shell's session. A host that cannot
// supply its identity (no operator directory) downgrades to a warning so
// plain agent registration keeps working; every refusal is returned.
func registerCurrentSession(ctx context.Context, agentID string, opts sessionOptions) (*sessionReport, error) {
	svc, err := newRegistrationService()
	if err != nil {
		return &sessionReport{Warning: err.Error()}, nil //nolint:nilerr // identity gaps downgrade to a warning by design
	}
	det, pid, start := currentSession(opts)
	res, err := svc.RegisterSession(ctx, service.RegisterSessionRequest{
		AgentID: agentID, Harness: det.Harness, SessionHandle: det.Handle, Origin: det.Origin,
		PID: pid, PIDStartTime: start, TakeOver: opts.TakeOver, HasTTY: destructiveStdinIsTerminal(),
	})
	if err != nil {
		return nil, fmt.Errorf("register session for %s: %w", agentID, err)
	}
	// A newer CLI that meets an older dispatcher restarts it or says why not (ADR-007 §6.4).
	productionDispatcherRestart(ctx, os.Stderr)
	r := res.Registration
	return &sessionReport{Outcome: res.Outcome, Harness: r.Harness, Origin: r.Origin, Epoch: r.Epoch, NoticeSent: res.NoticeSent, RoutingWarning: res.RoutingWarning}, nil
}

func newAgentUnregisterCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "unregister <agent-id>",
		Short: "Remove this host's session registration for an agent id",
		Long: `Remove the session registration for an agent id on this host. Run it from the
owning session when you move a lane. A row whose session has exited can be
removed by anyone; a live row of another session needs --force (operator).`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return runAgentUnregister(args[0], force) },
	}
	cmd.Flags().BoolVar(&force, "force", false, "Remove even a live session's registration")
	return cmd
}

func runAgentUnregister(agentID string, force bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}
	svc, err := newRegistrationService()
	if err != nil {
		return err
	}
	if err := svc.Unregister(context.Background(), agentID, currentIdentity(), force); err != nil {
		return err
	}
	if app.jsonOutput {
		return printJSON(map[string]string{"agent_id": agentID, "status": "unregistered"})
	}
	fmt.Printf("Unregistered agent %s\n", agentID)
	return nil
}

func newAgentListCmd() *cobra.Command {
	var registrations bool
	cmd := &cobra.Command{
		Use:   "list --registrations",
		Short: "List session registrations on this host",
		Long: `List the session registrations for this host and user: harness, origin,
permission mode, liveness (alive, dead or unknown), whether a live route
exists and whether the row is held. Use --json for machine-readable output.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if !registrations {
				return errors.New("only --registrations is available: run 'mtix agent list --registrations'")
			}
			return runAgentListRegistrations()
		},
	}
	cmd.Flags().BoolVar(&registrations, "registrations", false, "List session registrations")
	return cmd
}

func runAgentListRegistrations() error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}
	svc, err := newRegistrationService()
	if err != nil {
		return err
	}
	views, err := svc.ListRegistrations(context.Background())
	if err != nil {
		return err
	}
	if app.jsonOutput {
		return printJSON(views)
	}
	if len(views) == 0 {
		fmt.Println("No registrations on this host.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tHARNESS\tORIGIN\tMODE\tLIVENESS\tROUTE\tHOLD\tEPOCH\tLAST SEEN")
	for _, v := range views {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%t\t%d\t%s\n", v.AgentID, v.Harness, v.Origin,
			dash(v.PermissionMode), v.Liveness, v.LiveRoute, v.Hold, v.Epoch, v.LastSeen)
	}
	return w.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printJSON(v any) error {
	return NewOutputWriter(true).WriteJSON(v)
}
