// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// Operator alert classes raised by the attach verbs (ADR-007 §6.5, P1.8).
const (
	attachAlertProtocolSkew = "attach.protocol-skew"
	attachAlertVerbFailure  = "attach.verb-failure"
	// attachAlertAgent names alerts for a harness the verb could not identify.
	attachAlertAgent = "attach"
)

// Per-failure remedies recorded with each attach alert, so mtix doctor names
// an action rather than pointing back at itself (ADR-007 §7 exact fixes).
const (
	attachFixGlueEntry  = "correct or remove the mtix attach entries in the harness configuration"
	attachFixAdapterRun = "the session stays inbox-only; read its messages with mtix inbox"
)

// attachFixUnknownVerb is the remedy for a verb this build does not know.
const attachFixUnknownVerb = "upgrade mtix to the release that wrote the harness glue (compare with mtix version), or correct the mtix attach entries in the harness configuration"

// attachFixNoAdapter names the remedy when this build has no adapter for h.
func attachFixNoAdapter(h string) string {
	return "upgrade mtix to a build with the " + h + " adapter (compare mtix version with the glue), or remove the " + h + " glue from the harness configuration"
}

// attachAbortKey marks a verb whose pre-run already reported a failure.
type attachAbortKey struct{}

// attachDeps are the seams of the attach verbs; production values come from
// productionAttachDeps, tests inject their own. Nothing here is global.
type attachDeps struct {
	current  int
	adapters map[string]attach.Adapter
	state    func() (*oplocal.State, error)
	clock    func() time.Time
	// glueWriter reports whether this build can rewrite a harness's glue;
	// nil means it cannot.
	glueWriter func(harness string) bool
}

func productionAttachDeps() attachDeps {
	return attachDeps{
		current:    attach.Protocol,
		adapters:   map[string]attach.Adapter{}, // harness adapters ship with their harness integrations
		state:      operatorState,
		clock:      func() time.Time { return time.Now().UTC() },
		glueWriter: productionGlueWriter,
	}
}

// productionGlueWriter reports whether the production refresh registry,
// the one doctor --fix runs, has a writer for the harness's glue.
func productionGlueWriter(harness string) bool {
	st, err := operatorState()
	if err != nil {
		return false
	}
	reg, err := productionAttachRegistry(st, attach.Protocol)
	return err == nil && reg.Writer("glue."+harness) != nil
}

func (d attachDeps) hasGlueWriter(harness string) bool {
	return d.glueWriter != nil && d.glueWriter(harness)
}

func operatorState() (*oplocal.State, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return nil, fmt.Errorf("operator state: %w", err)
	}
	return oplocal.New(env), nil
}

func newAttachCmd() *cobra.Command { return newAttachCommand(productionAttachDeps()) }

// newAttachCommand builds the hidden verbs harness glue calls through the
// shim: mtix attach <verb> --harness H --protocol N (ADR-007 §6.5). A verb
// always exits 0 so a harness never blocks on it; every failure prints one
// line on stderr and raises an operator-local alert shown by mtix doctor.
func newAttachCommand(deps attachDeps) *cobra.Command {
	var harness string
	cmd := &cobra.Command{
		Use:    "attach",
		Short:  "Harness glue entry points (internal; called through the attach shim)",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		// Glue from a newer release may call a verb this build lacks; that is
		// a failure like any other: one stderr line, an alert, exit 0, and
		// nothing on stdout (a hook's stdout can reach the agent).
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(c *cobra.Command, args []string) error {
			if aborted, _ := c.Context().Value(attachAbortKey{}).(bool); aborted {
				return nil
			}
			agent, verb := attachAlertAgent, "(none)"
			if attach.KnownHarness(harness) {
				agent = harness
			}
			if len(args) > 0 {
				verb = args[0]
			}
			attachFailure(c.Context(), deps, c.ErrOrStderr(), agent, attachAlertVerbFailure,
				fmt.Sprintf("mtix attach: unknown attach verb %q in this mtix build; messages stay in the inbox (mtix inbox)", verb),
				attachFixUnknownVerb)
			return nil
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "", "Harness the glue belongs to")
	cmd.Flags().String("protocol", "", "Attach protocol the glue was written for")
	// A flag the verb cannot parse must not make the hook fail: report it,
	// raise an alert and exit 0. Verbs inherit this from the group.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		attachFailure(c.Context(), deps, c.ErrOrStderr(), attachAlertAgent, attachAlertVerbFailure,
			fmt.Sprintf("mtix attach %s: cannot parse the arguments (%v); messages stay in the inbox (mtix inbox)", c.Name(), err),
			attachFixGlueEntry)
		return nil
	})
	for _, verb := range attach.Verbs() {
		cmd.AddCommand(newAttachVerbCmd(deps, verb))
	}
	return cmd
}

func newAttachVerbCmd(deps attachDeps, verb string) *cobra.Command {
	var harness, protocol string
	cmd := &cobra.Command{
		Use:   verb + " --harness <harness> --protocol <n>",
		Short: "Attach verb " + verb + " (internal)",
		Args:  cobra.ArbitraryArgs,
		// Newer glue may pass flags this binary does not know; never fail on them.
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		SilenceErrors:      true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if aborted, _ := cmd.Context().Value(attachAbortKey{}).(bool); aborted {
				return nil // the pre-run already printed the line and raised the alert
			}
			runAttachVerb(cmd.Context(), deps, attach.VerbRequest{
				Verb: verb, Harness: harness, Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			}, protocol)
			return nil
		},
	}
	cmd.Flags().StringVar(&harness, "harness", "", "Harness the glue belongs to: claude-code, codex or pi")
	cmd.Flags().StringVar(&protocol, "protocol", "", "Attach protocol the glue was written for")
	return cmd
}

// runAttachVerb performs the handshake and runs the harness adapter. It never
// returns an error: failures are reported on stderr and as alerts.
func runAttachVerb(ctx context.Context, deps attachDeps, req attach.VerbRequest, protocol string) {
	if !attach.KnownHarness(req.Harness) {
		attachFailure(ctx, deps, req.Stderr, attachAlertAgent, attachAlertVerbFailure,
			fmt.Sprintf("mtix attach %s: unknown harness %q; messages stay in the inbox (mtix inbox)", req.Verb, req.Harness),
			attachFixGlueEntry)
		return
	}
	requested, err := strconv.Atoi(protocol)
	if err != nil {
		attachFailure(ctx, deps, req.Stderr, req.Harness, attachAlertVerbFailure,
			fmt.Sprintf("mtix attach %s: %s glue passed no valid --protocol; messages stay in the inbox (mtix inbox)", req.Verb, req.Harness),
			attachFixGlueEntry)
		return
	}
	switch attach.Negotiate(deps.current, requested) {
	case attach.DecisionOutOfRange:
		lo, hi := attach.SupportedRange(deps.current)
		fix := glueSkewFix(req.Harness, requested, deps.current, deps.hasGlueWriter(req.Harness))
		attachFailure(ctx, deps, req.Stderr, req.Harness, attachAlertProtocolSkew, fmt.Sprintf(
			"mtix attach: %s glue uses attach protocol %d, outside the supported range %d-%d; live wake is off and messages stay in the inbox (mtix inbox); fix: %s; see doctor check glue.%s",
			req.Harness, requested, lo, hi, fix, req.Harness), fix)
		if requested >= 0 { // a negative protocol is garbage, not a glue version to refresh from
			markGlue(ctx, deps, req, requested)
		}
		return
	case attach.DecisionServeAndRefresh:
		markGlue(ctx, deps, req, requested)
	}
	req.Protocol = requested
	recordGlueActivity(req.Verb) // MTIX-112.20.1: session start and prompt submit are activity
	runAttachAdapter(ctx, deps, req)
}

func runAttachAdapter(ctx context.Context, deps attachDeps, req attach.VerbRequest) {
	adapter, ok := deps.adapters[req.Harness]
	if !ok {
		attachFailure(ctx, deps, req.Stderr, req.Harness, attachAlertVerbFailure, fmt.Sprintf(
			"mtix attach %s: no %s adapter in this mtix build; live wake is off and messages stay in the inbox (mtix inbox)", req.Verb, req.Harness),
			attachFixNoAdapter(req.Harness))
		return
	}
	if err := adapter.Run(ctx, req); err != nil {
		attachFailure(ctx, deps, req.Stderr, req.Harness, attachAlertVerbFailure, fmt.Sprintf(
			"mtix attach %s: %s adapter failed: %v; messages stay in the inbox (mtix inbox)", req.Verb, req.Harness, err),
			attachFixAdapterRun)
	}
}

// markGlue records a refresh mark. A failure (a sandboxed CLI, unreadable
// operator state) is reported on stderr and raised as an alert; the glue is
// still served, and doctor reports the skew.
func markGlue(ctx context.Context, deps attachDeps, req attach.VerbRequest, seen int) {
	st, err := deps.state()
	if err == nil {
		err = attach.MarkGlueForRefresh(st, req.Harness, seen)
	}
	if err != nil {
		attachFailure(ctx, deps, req.Stderr, req.Harness, attachAlertVerbFailure,
			fmt.Sprintf("mtix attach %s: could not mark the %s glue for refresh: %v", req.Verb, req.Harness, err),
			operatorStateFix)
	}
}

// attachFailure prints the one-line notice and raises the alert. If the alert
// cannot be recorded, it says so instead of failing silently.
func attachFailure(ctx context.Context, deps attachDeps, stderr io.Writer, agent, class, line, fix string) {
	_, _ = fmt.Fprintln(stderr, line) //nolint:errcheck // stderr is the last-resort channel
	st, err := deps.state()
	if err == nil {
		_, err = service.NewAlerts(st, deps.clock).Raise(ctx, service.AlertRequest{
			Agent: agent, Class: class, Detail: line, Fix: fix,
		})
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "mtix attach: could not record an operator alert: %v\n", err) //nolint:errcheck // stderr is the last-resort channel
	}
}

// isAttachCommand reports whether cmd is the attach group or one of its verbs.
func isAttachCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "attach" && c.Parent() != nil && c.Parent().Parent() == nil {
			return true
		}
	}
	return false
}

// abortAttachVerb reports a pre-run failure of an attach verb (for example a
// -C directory that does not exist) the way every verb failure is reported,
// then marks the verb so it does nothing and still exits 0.
func abortAttachVerb(cmd *cobra.Command, line string) {
	agent := attachAlertAgent
	if h, err := cmd.Flags().GetString("harness"); err == nil && attach.KnownHarness(h) {
		agent = h
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	attachFailure(ctx, productionAttachDeps(), cmd.ErrOrStderr(), agent, attachAlertVerbFailure, line, attachFixGlueEntry)
	cmd.SetContext(context.WithValue(ctx, attachAbortKey{}, true))
}
