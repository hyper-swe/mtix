// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// errInboxWaitEmpty signals that `mtix inbox --wait` timed out with no events.
// It is silenced (root SilenceErrors) and mapped to exitCodeInboxEmpty so a
// worker loop can branch on the exit code without parsing output (FR-19.4).
var errInboxWaitEmpty = errors.New("inbox: wait timed out with no events")

// newInboxCmd creates the mtix inbox command (FR-19.4 / MTIX-47.1): a per-agent
// view of comments addressed to it, DERIVED from the event journal (not a
// separate mailbox), past the agent's ack cursor.
func newInboxCmd() *cobra.Command {
	var (
		agent   string
		wait    bool
		count   bool
		all     bool
		timeout int
		format  string
	)
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Show events addressed to an agent",
		Long:  inboxLongHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateInboxCountFlags(cmd); err != nil {
				return err
			}
			if count {
				return runInboxCount(cmd, agent, all)
			}
			return runInbox(agent, wait, timeout, format)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id whose inbox to read (required)")
	cmd.Flags().BoolVar(&count, "count", false, "Print pending count without acknowledging or touching sessions")
	cmd.Flags().BoolVar(&all, "all", false, "With --count, print total retained events including acknowledged events")
	cmd.Flags().BoolVar(&wait, "wait", false, "Long-poll: block until an event arrives or --timeout")
	cmd.Flags().IntVar(&timeout, "timeout", 300, "Max seconds to block with --wait")
	cmd.Flags().StringVar(&format, "format", "",
		"Agent-ready output: 'prompt' (wake payload) or 'context' (hook injection)")
	_ = cmd.MarkFlagRequired("agent")
	cmd.AddCommand(newInboxAckCmd(), newInboxLeaseCmd())
	return cmd
}

func newInboxAckCmd() *cobra.Command {
	var (
		agent   string
		through bool
	)
	cmd := &cobra.Command{
		Use:   "ack <seq>...",
		Short: "Acknowledge specific inbox events (selective); --through acks everything up to a seq",
		Long: `Acknowledge inbox events. By default this is SELECTIVE: only the
given sequence(s) are marked seen, so acking a higher seq never drops
lower, still-unprocessed events — an event you do not ack simply
reappears on the next list (defer by not acking).

Use --through to acknowledge everything up to and including a single
sequence at once (a watermark), for a worker that processes strictly
in order.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runInboxAck(agent, args, through)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id (required)")
	cmd.Flags().BoolVar(&through, "through", false,
		"Acknowledge every event up to and including the given seq (watermark), not just that seq")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func runInbox(agent string, wait bool, timeout int, format string) error {
	if format != "" && format != "prompt" && format != "context" {
		return fmt.Errorf("unknown --format %q (want 'prompt' or 'context')", format)
	}
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	ctx := sessionContext(context.Background()) // refreshes last_seen for the registered session

	var (
		events []sqlite.InboxEvent
		err    error
	)
	if wait {
		events, err = app.store.InboxWait(ctx, agent, time.Duration(timeout)*time.Second)
	} else {
		events, err = app.store.InboxList(ctx, agent)
	}
	if err != nil {
		return err
	}

	if err := writeInbox(agent, format, events); err != nil {
		return err
	}

	// FR-19.4: an empty --wait timeout surfaces as a distinct exit code.
	if wait && len(events) == 0 {
		return errInboxWaitEmpty
	}
	return nil
}

// writeInbox renders the inbox in the selected shape: agent-ready formats
// (MTIX-56.8), JSON, or the human listing.
func writeInbox(agent, format string, events []sqlite.InboxEvent) error {
	out := NewOutputWriter(app.jsonOutput)
	if format != "" {
		caps, err := loadInboxEnvelopeCaps()
		if err != nil {
			return err
		}
		var text string
		if format == "prompt" {
			text, err = formatInboxPrompt(agent, events, caps)
		} else {
			text, err = formatInboxContext(agent, events, caps)
		}
		if err != nil {
			return fmt.Errorf("render inbox: %w", err)
		}
		out.WriteHuman("%s", text)
		return nil
	}
	if app.jsonOutput {
		if events == nil {
			events = []sqlite.InboxEvent{}
		}
		return out.WriteJSON(events)
	}
	if len(events) == 0 {
		out.WriteHuman("(inbox empty)\n")
		return nil
	}
	for _, e := range events {
		out.WriteHuman("[%d] %s  %s: %s\n", e.Seq, e.NodeID, e.Author, e.Body)
	}
	out.WriteHuman("\nack with: mtix inbox ack %d --agent %s\n", events[len(events)-1].Seq, agent)
	return nil
}

func loadInboxEnvelopeCaps() (inboxformat.Caps, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return inboxformat.Caps{}, fmt.Errorf("inbox operator state: %w", err)
	}
	state := oplocal.New(env)
	caps, reports, err := state.EnvelopeCaps()
	if err != nil {
		return inboxformat.Caps{}, fmt.Errorf("inbox envelope configuration: %w; %s", err, state.EnvelopeCapsRemedy(reports...))
	}
	return caps, nil
}

func inboxEnvelopeEvents(events []sqlite.InboxEvent) []inboxformat.Event {
	out := make([]inboxformat.Event, len(events))
	for i, event := range events {
		out[i] = inboxformat.Event{Seq: event.Seq, Task: event.NodeID, Sender: event.Author, Body: event.Body}
	}
	return out
}

// formatInboxPrompt composes only a fixed nudge and the quoted envelope
// (FR-20.2, ADR-007 I33). Empty inbox remains empty for wake-script checks.
func formatInboxPrompt(agent string, events []sqlite.InboxEvent, caps inboxformat.Caps) (string, error) {
	return inboxformat.RenderPrompt(agent, inboxEnvelopeEvents(events), caps)
}

// formatInboxContext uses the shared envelope and revalidated command tokens
// (FR-20.2, ADR-007 I12/I13), without acknowledging or changing the inbox.
func formatInboxContext(agent string, events []sqlite.InboxEvent, caps inboxformat.Caps) (string, error) {
	return inboxformat.RenderEnvelope(agent, inboxEnvelopeEvents(events), caps)
}

func runInboxAck(agent string, seqs []string, through bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	parsed := make([]int64, 0, len(seqs))
	var maxSeq int64
	for _, s := range seqs {
		n, parseErr := strconv.ParseInt(s, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("invalid sequence %q: %w", s, parseErr)
		}
		parsed = append(parsed, n)
		if n > maxSeq {
			maxSeq = n
		}
	}
	ctx := sessionContext(context.Background()) // refreshes last_seen for the registered session
	out := NewOutputWriter(app.jsonOutput)

	if through {
		// Cumulative: mark everything up to the highest given seq as seen.
		if err := app.store.InboxAckThrough(ctx, agent, maxSeq); err != nil {
			return err
		}
		if app.jsonOutput {
			return out.WriteJSON(map[string]any{"agent": agent, "acked_through": maxSeq})
		}
		out.WriteHuman("✓ %s acked through %d\n", agent, maxSeq)
		return nil
	}

	// Selective (default): ack exactly the given seq(s); unacked events resurface.
	for _, n := range parsed {
		if err := app.store.InboxAck(ctx, agent, n); err != nil {
			return err
		}
	}
	if app.jsonOutput {
		return out.WriteJSON(map[string]any{"agent": agent, "acked": parsed})
	}
	out.WriteHuman("✓ %s acked %v\n", agent, parsed)
	return nil
}
