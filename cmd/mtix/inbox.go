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
	)
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Show events addressed to an agent",
		Long: `Show comments addressed to an agent (via 'mtix comment --to <agent>'),
oldest first, past the agent's ack cursor.

With --wait, block until a new addressed event arrives — the primitive a worker's
outer loop parks on between tasks — or until --timeout seconds elapse. Exit 0
when events are returned; exit 5 on an empty timeout (so a loop can distinguish
"woke with work" from "nothing yet").`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInbox(agent, wait, timeout)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id whose inbox to read (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Long-poll: block until an event arrives or --timeout")
	cmd.Flags().IntVar(&timeout, "timeout", 300, "Max seconds to block with --wait")
	_ = cmd.MarkFlagRequired("agent")
	cmd.AddCommand(newInboxAckCmd())
	return cmd
}

func newInboxAckCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "ack <seq>...",
		Short: "Acknowledge inbox events up to the highest given sequence",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runInboxAck(agent, args)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id (required)")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func runInbox(agent string, wait bool, timeout int) error {
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

	out := NewOutputWriter(app.jsonOutput)
	switch {
	case app.jsonOutput:
		if events == nil {
			events = []sqlite.InboxEvent{}
		}
		if writeErr := out.WriteJSON(events); writeErr != nil {
			return writeErr
		}
	case len(events) == 0:
		out.WriteHuman("(inbox empty)\n")
	default:
		for _, e := range events {
			out.WriteHuman("[%d] %s  %s: %s\n", e.Seq, e.NodeID, e.Author, e.Body)
		}
		out.WriteHuman("\nack with: mtix inbox ack %d --agent %s\n", events[len(events)-1].Seq, agent)
	}

	// FR-19.4: an empty --wait timeout surfaces as a distinct exit code.
	if wait && len(events) == 0 {
		return errInboxWaitEmpty
	}
	return nil
}

func runInboxAck(agent string, seqs []string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	// Ack is a watermark: advance to the highest sequence supplied.
	var maxSeq int64
	for _, s := range seqs {
		n, parseErr := strconv.ParseInt(s, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("invalid sequence %q: %w", s, parseErr)
		}
		if n > maxSeq {
			maxSeq = n
		}
	}
	if err := app.store.InboxAck(context.Background(), agent, maxSeq); err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(map[string]any{"agent": agent, "acked_through": maxSeq})
	}
	out.WriteHuman("✓ %s acked through %d\n", agent, maxSeq)
	return nil
}
