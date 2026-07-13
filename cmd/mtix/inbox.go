// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

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
		timeout int
		format  string
	)
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Show events addressed to an agent",
		Long: `Show comments addressed to an agent (via 'mtix comment --to <agent>'),
oldest first, past the agent's ack cursor.

With --wait, block until a new addressed event arrives — the primitive a worker's
outer loop parks on between tasks — or until --timeout seconds elapse. Exit 0
when events are returned; exit 5 on an empty timeout (so a loop can distinguish
"woke with work" from "nothing yet").

--format emits agent-ready text instead of the human listing (FR-20 §9,
"delivery terminates in the prompt"):
  prompt   a complete opening prompt for a cold-started agent — the events
           verbatim plus the ack/reply contract. A wake exec launches the
           harness CLI with this as the prompt.
  context  a compact block for harness context-injection hooks
           (session-start / prompt-submit).
Both print NOTHING when the inbox is empty, so a wake script's idempotency
check is: payload=$(mtix inbox --agent X --format prompt); [ -z "$payload" ].
Reads never ack — acknowledgement stays explicit.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInbox(agent, wait, timeout, format)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id whose inbox to read (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Long-poll: block until an event arrives or --timeout")
	cmd.Flags().IntVar(&timeout, "timeout", 300, "Max seconds to block with --wait")
	cmd.Flags().StringVar(&format, "format", "",
		"Agent-ready output: 'prompt' (wake payload) or 'context' (hook injection)")
	_ = cmd.MarkFlagRequired("agent")
	cmd.AddCommand(newInboxAckCmd())
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
	ctx := context.Background()

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
	switch {
	case format == "prompt":
		out.WriteHuman("%s", formatInboxPrompt(agent, events))
	case format == "context":
		out.WriteHuman("%s", formatInboxContext(agent, events))
	case app.jsonOutput:
		if events == nil {
			events = []sqlite.InboxEvent{}
		}
		return out.WriteJSON(events)
	case len(events) == 0:
		out.WriteHuman("(inbox empty)\n")
	default:
		for _, e := range events {
			out.WriteHuman("[%d] %s  %s: %s\n", e.Seq, e.NodeID, e.Author, e.Body)
		}
		out.WriteHuman("\nack with: mtix inbox ack %d --agent %s\n", events[len(events)-1].Seq, agent)
	}
	return nil
}

// formatInboxPrompt renders the inbox as a complete opening prompt for a
// cold-started agent (FR-20 §9 rung 1): the events verbatim plus the ack and
// reply contracts, so handling terminates in the store, not in a human relay.
// Empty inbox -> empty string (the wake script's idempotency check).
func formatInboxPrompt(agent string, events []sqlite.InboxEvent) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You are agent %q. You have %d unread event(s) in your mtix inbox.\n", agent, len(events))
	fmt.Fprintf(&b, "Handle each event, then acknowledge it: mtix inbox ack <seq> --agent %s\n", agent)
	b.WriteString("Reply to a sender when needed: mtix comment <node-id> --to <sender> \"<message>\"\n")
	b.WriteString("Inspect a task and its context chain: mtix show <node-id>; mtix context <node-id>\n\n")
	for _, e := range events {
		fmt.Fprintf(&b, "[seq %d] %s from %s: %s\n", e.Seq, e.NodeID, e.Author, e.Body)
	}
	return b.String()
}

// formatInboxContext renders the inbox as a compact block for harness
// context-injection hooks (FR-20 §9 rung 2) — one line per event plus the ack
// contract. Empty inbox -> empty string, so hooks inject nothing when there is
// nothing to say.
func formatInboxContext(agent string, events []sqlite.InboxEvent) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mtix inbox for agent %q: %d unread event(s). Ack after handling: mtix inbox ack <seq> --agent %s\n",
		agent, len(events), agent)
	for _, e := range events {
		fmt.Fprintf(&b, "- [%d] %s from %s: %s\n", e.Seq, e.NodeID, e.Author, e.Body)
	}
	return b.String()
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
	ctx := context.Background()
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
