// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/hooks"
)

// knownFireEvents is the canonical FR-19.2 event set `hooks fire` accepts. It is
// the single source of truth for both the membership check and the "want one of"
// error text below, so the two never drift. (The domain package validates the
// same set internally when loading hooks.yaml, but keeps its predicate
// unexported; this mirrors it for the CLI's --event flag.)
var knownFireEvents = []string{
	hooks.EventCommentAddressed,
	hooks.EventStatusChanged,
	hooks.EventNodeCreated,
}

// newHooksCmd creates the `mtix hooks` command group per FR-19.7 (MTIX-47.8),
// the config-facing observability subset: inspect the declared hooks and test a
// hooks.yaml against a sample event. It is read-only (plus --dry-run), so it is
// deliberately absent from isMutationCommand. The runtime `hooks log` view needs
// the dispatcher and lands separately.
func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect and test FR-19 event hooks (.mtix/hooks.yaml)",
	}
	cmd.AddCommand(newHooksListCmd(), newHooksFireCmd(), newHooksTrustCmd(), newHooksLogCmd(), newHooksExecDispatchCmd())
	return cmd
}

// newHooksLogCmd creates `mtix hooks log` (FR-19.7): the audit trail of hook
// firings — event id, hook, adapter, and outcome (delivered / error /
// skipped-untrusted) — newest first.
func newHooksLogCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show recent hook firings (audit trail, newest first)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHooksLog(limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Max entries to show")
	return cmd
}

func runHooksLog(limit int) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}
	entries, err := app.store.ReadHookLog(context.Background(), limit)
	if err != nil {
		return err
	}
	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(entries)
	}
	if len(entries) == 0 {
		out.WriteHuman("(no hook firings recorded)\n")
		return nil
	}
	for _, e := range entries {
		detail := ""
		if e.Detail != "" {
			detail = "  (" + e.Detail + ")"
		}
		out.WriteHuman("%s  %s  %s  %s → %s  [%s]%s\n",
			e.FiredAt, e.Hook, e.NodeID, e.Event, e.Adapter, e.Outcome, detail)
	}
	return nil
}

// newHooksTrustCmd creates `mtix hooks trust` (MTIX-47.5): record the current
// .mtix/hooks.yaml as trusted to run its exec hooks. Exec runs local commands,
// so it is OFF until trusted; trust binds to the file's content hash and is
// LOCAL to this machine (never committed or synced), so any later edit — yours
// or a teammate's arriving via sync — voids it until you review and re-trust.
func newHooksTrustCmd() *cobra.Command {
	var status bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Trust the current .mtix/hooks.yaml to run exec hooks (content-hash pinned, local)",
		Long: `Record the current .mtix/hooks.yaml as trusted to run its exec hooks.

The trust pin covers hooks.yaml AND the content of every local script an
exec hook runs (MTIX-49). Editing either the config or a wake-script voids
trust — exec is skipped until you review and re-run 'mtix hooks trust' —
so an approve-then-swap-the-payload change cannot ride a stale approval.
The pin is local, gitignored, and never synced.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHooksTrust(status)
		},
	}
	cmd.Flags().BoolVar(&status, "status", false, "Show whether the current hooks.yaml is trusted, without changing it")
	return cmd
}

func runHooksTrust(status bool) error {
	if app.mtixDir == "" {
		return fmt.Errorf("not in an mtix project")
	}
	cur := hooks.ConfigHash(app.mtixDir)
	if cur == "" {
		return fmt.Errorf("no .mtix/hooks.yaml to trust")
	}
	out := NewOutputWriter(app.jsonOutput)

	if status {
		trusted := hooks.ExecTrusted(app.mtixDir)
		printExecHooks(app.mtixDir)
		if app.jsonOutput {
			return out.WriteJSON(map[string]any{"trusted": trusted, "hash": cur})
		}
		if trusted {
			out.WriteHuman("✓ hooks.yaml is trusted for exec (hash %s)\n", cur[:12])
		} else {
			out.WriteHuman("✗ hooks.yaml is NOT trusted for exec — review the above and run 'mtix hooks trust'\n")
		}
		return nil
	}

	// Show the exec hooks being trusted (for review) BEFORE pinning the hash.
	printExecHooks(app.mtixDir)
	if err := hooks.SaveTrust(app.mtixDir, cur); err != nil {
		return fmt.Errorf("save trust: %w", err)
	}
	if app.jsonOutput {
		return out.WriteJSON(map[string]any{"trusted": true, "hash": cur})
	}
	out.WriteHuman("✓ trusted .mtix/hooks.yaml for exec (hash %s). Re-run 'mtix hooks trust' after any edit.\n", cur[:12])
	return nil
}

// printExecHooks lists the exec hooks in hooks.yaml (argv + events) on stderr so
// the operator reviews exactly what they are trusting before it can run.
func printExecHooks(mtixDir string) {
	cfg, _ := hooks.Load(mtixDir)
	n := 0
	for _, h := range cfg.Hooks {
		if !slices.Contains(h.Deliver, hooks.AdapterExec) || h.Exec == nil {
			continue
		}
		n++
		fmt.Fprintf(os.Stderr, "  exec hook %q: %v  (events: %s)\n",
			h.Name, h.Exec.Command, strings.Join(h.Match.Events, ", "))
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "  (no exec hooks in this config)")
	}
}

// newHooksListCmd creates `mtix hooks list`, which loads .mtix/hooks.yaml and
// prints each hook with its subscribed events and delivery adapters. Honors the
// global --json flag.
func newHooksListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured hooks with their events and delivery adapters",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHooksList()
		},
	}
}

// runHooksList loads and lists the configured hooks. hooks.Load never fails; any
// validation warnings for dropped hooks are surfaced on stderr so the operator
// can see which hook was disabled and why.
func runHooksList() error {
	if app.mtixDir == "" {
		return fmt.Errorf("not in an mtix project")
	}

	cfg, warnings := hooks.Load(app.mtixDir)
	printHookWarnings(warnings)

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		type hookOut struct {
			Name    string   `json:"name"`
			Events  []string `json:"events"`
			Deliver []string `json:"deliver"`
		}
		list := make([]hookOut, 0, len(cfg.Hooks))
		for _, h := range cfg.Hooks {
			list = append(list, hookOut{Name: h.Name, Events: h.Match.Events, Deliver: h.Deliver})
		}
		return out.WriteJSON(list)
	}

	if len(cfg.Hooks) == 0 {
		out.WriteHuman("(no hooks configured)\n")
		return nil
	}
	for _, h := range cfg.Hooks {
		out.WriteHuman("%s\n", h.Name)
		out.WriteHuman("  events:  %s\n", strings.Join(h.Match.Events, ", "))
		out.WriteHuman("  deliver: %s\n", strings.Join(h.Deliver, ", "))
	}
	return nil
}

// newHooksFireCmd creates `mtix hooks fire`, which builds a sample event from the
// flags and reports which configured hooks would match it and which adapters each
// would deliver to. Requires --dry-run: real delivery needs the dispatcher, which
// is built separately, so any non-dry-run invocation is a no-op with a note.
func newHooksFireCmd() *cobra.Command {
	var (
		event     string
		node      string
		toAgent   string
		fromAgent string
		statusTo  string
		synced    bool
		dryRun    bool
	)
	cmd := &cobra.Command{
		Use:   "fire",
		Short: "Test hooks.yaml against a sample event (dry-run only, for now)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runHooksFire(event, node, toAgent, fromAgent, statusTo, synced, dryRun)
		},
	}
	cmd.Flags().StringVar(&event, "event", "",
		"event name (required): comment.addressed, status.changed, node.created")
	cmd.Flags().StringVar(&node, "node", "", "affected node id (drives the `under` subtree filter)")
	cmd.Flags().StringVar(&toAgent, "to", "", "addressee agent (drives the to-agent filter)")
	cmd.Flags().StringVar(&fromAgent, "from", "", "origin/author agent (drives the from-agent-not filter)")
	cmd.Flags().StringVar(&statusTo, "status-to", "", "new status (drives the status-to filter)")
	cmd.Flags().BoolVar(&synced, "synced", false, "treat the event as arriving via hub replication")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"preview matches without delivering (currently the only supported mode)")
	_ = cmd.MarkFlagRequired("event")
	return cmd
}

// runHooksFire matches a sample event (built from the flags) against the
// configured hooks and reports the matches. Delivery is not performed: only
// --dry-run is supported until the dispatcher lands.
func runHooksFire(event, node, toAgent, fromAgent, statusTo string, synced, dryRun bool) error {
	if app.mtixDir == "" {
		return fmt.Errorf("not in an mtix project")
	}
	if !slices.Contains(knownFireEvents, event) {
		return fmt.Errorf("unknown event %q (want one of: %s)", event, strings.Join(knownFireEvents, ", "))
	}
	if !dryRun {
		fmt.Fprintln(os.Stderr,
			"hooks fire: only --dry-run is supported until the dispatcher lands; no hooks were delivered")
		return nil
	}

	cfg, warnings := hooks.Load(app.mtixDir)
	printHookWarnings(warnings)

	evt := hooks.Event{
		Name:     event,
		NodeID:   node,
		Author:   fromAgent,
		ToAgent:  toAgent,
		StatusTo: statusTo,
		Synced:   synced,
	}
	matched := cfg.MatchingHooks(evt)

	out := NewOutputWriter(app.jsonOutput)

	if app.jsonOutput {
		type matchOut struct {
			Name    string   `json:"name"`
			Deliver []string `json:"deliver"`
		}
		list := make([]matchOut, 0, len(matched))
		for _, h := range matched {
			list = append(list, matchOut{Name: h.Name, Deliver: h.Deliver})
		}
		return out.WriteJSON(map[string]any{"event": event, "matched": list})
	}

	if len(matched) == 0 {
		out.WriteHuman("(no hooks match this event)\n")
		return nil
	}
	out.WriteHuman("%d hook(s) match %s:\n", len(matched), event)
	for _, h := range matched {
		out.WriteHuman("  %s -> %s\n", h.Name, strings.Join(h.Deliver, ", "))
	}
	return nil
}

// printHookWarnings surfaces hooks.Load validation warnings on stderr, one per
// line, so a dropped/disabled hook is visible without failing the command.
func printHookWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}
}
