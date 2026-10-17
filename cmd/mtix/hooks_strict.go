// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// newHooksStrictCmd creates `mtix hooks strict on|off|status`, the operator
// switch for strict program mode. The setting is operator-local state, never a
// workspace key. on and off need a terminal and are refused under an agent
// session marker; status needs neither and supports --json.
func newHooksStrictCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "strict <on|off|status>",
		Short:     "Show or change strict program mode for exec hooks on this host",
		Long:      "Strict program mode is an opt-in, host-level setting stored in operator-local state. When on, an exec hook whose program is not a pinned project script or a known interpreter running one launches only after the operator approves that program with: mtix hooks trust --program <hook>. on and off require terminal stdin outside an agent session. status works without a terminal and supports --json.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"on", "off", "status"},
		RunE: func(_ *cobra.Command, args []string) error {
			env, err := oplocal.FromProcess()
			if err != nil {
				return fmt.Errorf("operator inputs: %w", err)
			}
			return runHooksStrict(oplocal.New(env), args[0], hookStdinTerminal(), hooks.SessionMarked())
		},
	}
}

func runHooksStrict(state *oplocal.State, action string, terminal, session bool) error {
	approvals := hooks.NewApprovals(app.mtixDir, state, session)
	switch action {
	case "on", "off":
		if err := approvals.SetStrict(action == "on", terminal); err != nil {
			return err
		}
	case "status":
	default:
		return fmt.Errorf("unknown action %q: use on, off or status", action)
	}
	cfg, _ := hooks.Load(app.mtixDir)
	status := approvals.StrictStatus(cfg)
	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(status)
	}
	renderStrictStatus(out, status)
	return nil
}

func renderStrictStatus(out OutputWriter, status hooks.StrictStatus) {
	out.WriteHuman("Strict program mode: %t\n%s\n", status.Enabled, status.Detail)
	for _, p := range status.Programs {
		out.WriteHuman("%s: approved=%t program=%s (%s)\n", p.Hook, p.Approved, p.Program, p.Detail)
	}
	if status.Fix != "" {
		out.WriteHuman("Fix: %s\n", status.Fix)
	}
}

// runHooksTrustProgram approves one exec hook's program for strict program
// mode (or, with status, only reports it). The hook itself is approved
// separately by mtix hooks trust <hook>.
func runHooksTrustProgram(approvals *hooks.Approvals, name string, status, terminal bool) error {
	cfg, warnings := hooks.Load(app.mtixDir)
	printHookWarnings(warnings)
	var selected *hooks.Hook
	for i := range cfg.Hooks {
		if cfg.Hooks[i].Name == name && cfg.Hooks[i].Exec != nil && slices.Contains(cfg.Hooks[i].Deliver, hooks.AdapterExec) {
			selected = &cfg.Hooks[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("configured exec hook %q not found", name)
	}
	if !status {
		show := func(program string) error {
			if _, err := fmt.Fprintf(os.Stderr, "Program to approve for hook %q: %s\n", name, program); err != nil {
				return fmt.Errorf("show program: %w", err)
			}
			return nil
		}
		if _, err := approvals.ApproveProgramShowing(*selected, terminal, show); err != nil {
			return err
		}
	}
	out := NewOutputWriter(app.jsonOutput)
	st := approvals.StrictStatus(hooks.Config{Hooks: []hooks.Hook{*selected}})
	if app.jsonOutput {
		return out.WriteJSON(st)
	}
	renderStrictStatus(out, st)
	return nil
}
