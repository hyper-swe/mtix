// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	inboxformat "github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// isInboxHistoryCommand is true for the root inbox command run with
// --history: a local, read-only audit that skips auto-import and server
// routing exactly as --count does, and contacts no hub (MTIX-112.2).
func isInboxHistoryCommand(cmd *cobra.Command) bool {
	flag := cmd.Flags().Lookup("history")
	return autoImportCommandPath(cmd) == "inbox" && flag != nil && flag.Value.String() == "true"
}

// validateInboxHistoryFlags refuses --history with the flags that read,
// wait or count, so one run answers one question.
func validateInboxHistoryFlags(cmd *cobra.Command) error {
	if !isInboxHistoryCommand(cmd) {
		return nil
	}
	for _, flag := range []string{"wait", "format", "count", "all"} {
		if cmd.Flags().Changed(flag) {
			return fmt.Errorf("--history cannot be combined with --%s: %w", flag, model.ErrInvalidInput)
		}
	}
	agent, err := cmd.Flags().GetString("agent")
	if err != nil {
		return fmt.Errorf("history agent flag: %w", err)
	}
	return service.ValidateInboxAgent(agent)
}

// runInboxHistory prints every event ever addressed to agent with its state
// and ack time. Read-only: it acks nothing and opens no hub connection.
func runInboxHistory(cmd *cobra.Command, agent string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	entries, err := service.NewInboxService(app.store).InboxHistory(cmd.Context(), agent)
	if err != nil {
		return fmt.Errorf("inbox history: %w", err)
	}
	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		if entries == nil {
			entries = []sqlite.InboxHistoryEntry{}
		}
		return out.WriteJSON(entries)
	}
	if len(entries) == 0 {
		out.WriteHuman("(nothing was ever addressed to %s)\n", inboxformat.SafeLine(agent))
		return nil
	}
	for _, e := range entries {
		out.WriteHuman("[%d] %s  %s  %s: %s\n", e.Seq, inboxformat.SafeLine(e.NodeID), historyState(e),
			inboxformat.SafeLine(e.Author), indentContinuation(inboxformat.SafeBlock(e.Body), inboxBodyIndent))
	}
	return nil
}

// historyState renders the ack state of one entry on one line.
func historyState(e sqlite.InboxHistoryEntry) string {
	switch {
	case e.State == sqlite.InboxAcked && e.AckedAt != nil:
		return "acked at " + e.AckedAt.UTC().Format(time.RFC3339)
	case e.State == sqlite.InboxAcked:
		return "acked (time unreadable)"
	case e.State == sqlite.InboxAckedThrough:
		return "acked through a watermark (time not kept)"
	default:
		return "unacked"
	}
}
