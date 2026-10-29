// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// errInboxWaitEmpty signals that `mtix inbox --wait` timed out with no events.
// It is silenced (root SilenceErrors) and mapped to exitCodeInboxEmpty so a
// worker loop can branch on the exit code without parsing output (FR-19.4).
var errInboxWaitEmpty = errors.New("inbox: wait timed out with no events")

// errInboxWaitInterrupted maps SIGINT/SIGTERM during a wait to exit 7 (FR-19).
var errInboxWaitInterrupted = errors.New("inbox: wait interrupted by signal")

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
	cmd.Flags().IntVar(&timeout, "timeout", 300, "Wait budget in seconds from entry; initial pending read may take longer (0 = no limit; negative refused)")
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
	if timeout < 0 || int64(timeout) > math.MaxInt64/int64(time.Second) {
		return fmt.Errorf("inbox: --timeout must be non-negative and fit a duration: %w", model.ErrInvalidInput)
	}
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
		events, err = waitInbox(ctx, agent, time.Duration(timeout)*time.Second)
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

// waitInbox translates OS signal cancellation to the documented exit-7
// sentinel while preserving storage failures (FR-19).
func waitInbox(ctx context.Context, agent string, timeout time.Duration) ([]sqlite.InboxEvent, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	events, err := app.store.InboxWait(ctx, agent, timeout)
	if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
		return nil, errInboxWaitInterrupted
	}
	return events, err
}

// inboxBodyIndent indents continuation lines of an inbox body under its entry.
const inboxBodyIndent = "    "

// writeInbox renders the inbox in the selected shape: agent-ready formats
// (MTIX-56.8), JSON, or the human listing.
func writeInbox(agent, format string, events []sqlite.InboxEvent) error {
	out := NewOutputWriter(app.jsonOutput)
	if format != "" {
		caps, err := loadInboxEnvelopeCaps(agent)
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
	boardArg, err := inboxformat.BoardSelection(inboxBoardRoot())
	if err != nil {
		return fmt.Errorf("render inbox: %w", err)
	}
	for _, e := range events {
		// Author and node id are one-line text; the body keeps its lines but
		// every continuation line is indented, so a stored body cannot forge
		// another entry or the ack line (MTIX-187.7.13).
		out.WriteHuman("[%d] %s  %s: %s\n", e.Seq, inboxformat.SafeLine(e.NodeID), inboxformat.SafeLine(e.Author),
			indentContinuation(inboxformat.SafeBlock(e.Body), inboxBodyIndent))
	}
	out.WriteHuman("\nack with: mtix inbox ack %d --agent %s%s\n", events[len(events)-1].Seq, agent, boardArg)
	return nil
}

// inboxBoardRoot is the absolute project root of the board this command opened
// (after any -C), or "" when no board is open. Inbox output pins every printed
// mtix command to it, so an agent acting from another repository, which has
// its own .mtix, still acts on the board it read (MTIX-189).
func inboxBoardRoot() string {
	if app.mtixDir == "" {
		return ""
	}
	return filepath.Dir(app.mtixDir)
}

// explainAckBoard names the board an ack checked when the seq is outside its
// journal (MTIX-189): such a seq almost always came from reading another
// board, for example through -C, while the ack ran from a repository with its
// own .mtix. The wrapped error keeps its class and exit code.
func explainAckBoard(err error) error {
	if !errors.Is(err, sqlite.ErrSeqOutsideJournal) {
		return err
	}
	return fmt.Errorf("%w\nboard checked: %s\nthe event may belong to another board: read and ack with the same board selection, for example mtix -C <board> inbox ack <seq> --agent <id>",
		err, inboxBoardRoot())
}

func loadInboxEnvelopeCaps(agents ...string) (inboxformat.Caps, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return inboxformat.Caps{}, fmt.Errorf("inbox operator state: %w", err)
	}
	state := oplocal.New(env)
	agent := ""
	if len(agents) > 0 {
		agent = agents[0]
	}
	caps, reports, err := state.EnvelopeCapsForAgent(agent)
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
	return inboxformat.RenderPromptOn(agent, inboxBoardRoot(), inboxEnvelopeEvents(events), caps)
}

// formatInboxContext uses the shared envelope and revalidated command tokens
// (FR-20.2, ADR-007 I12/I13), without acknowledging or changing the inbox.
func formatInboxContext(agent string, events []sqlite.InboxEvent, caps inboxformat.Caps) (string, error) {
	return inboxformat.RenderEnvelopeOn(agent, inboxBoardRoot(), inboxEnvelopeEvents(events), caps)
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
	inbox := service.NewInboxService(app.store)

	if through {
		// Cumulative: mark everything up to the highest given seq as seen.
		if err := inbox.InboxAckThrough(ctx, agent, maxSeq); err != nil {
			return explainAckBoard(err)
		}
		if app.jsonOutput {
			return out.WriteJSON(map[string]any{"agent": agent, "acked_through": maxSeq})
		}
		out.WriteHuman("✓ %s acked through %d\n", agent, maxSeq)
		return nil
	}

	// Selective (default): ack exactly the given seq(s); unacked events resurface.
	for _, n := range parsed {
		if err := inbox.InboxAck(ctx, agent, n); err != nil {
			return explainAckBoard(err)
		}
	}
	if app.jsonOutput {
		return out.WriteJSON(map[string]any{"agent": agent, "acked": parsed})
	}
	out.WriteHuman("✓ %s acked %v\n", agent, parsed)
	return nil
}
