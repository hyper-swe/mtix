// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// unackedInboxLabel names unread mail in the destructive-command summary.
const unackedInboxLabel = "unacknowledged inbox events (addressed mail no agent has acked)"

// unackedInboxCount is the count the destructive commands print before they
// run (MTIX-112.2): mail addressed to any agent and not yet acknowledged.
func unackedInboxCount(ctx context.Context) (destroyCount, error) {
	n, err := app.store.UnackedInboxEvents(ctx)
	if err != nil {
		return destroyCount{}, fmt.Errorf("count unacknowledged inbox events: %w", err)
	}
	return destroyCount{Label: unackedInboxLabel, N: int(n)}, nil
}

// writeExportInboxNote tells the operator, on stderr, that an export does not
// carry the inbox. The journal and the acks are per store: tasks.json holds
// tickets and annotations, so an import into another store starts with an
// empty inbox.
func writeExportInboxNote(w io.Writer, n int64) {
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "note: this store holds %d unacknowledged inbox event(s); the export does not carry them "+
		"(the event journal is per store and reaches another checkout only through sync, never through tasks.json). "+
		"List what was addressed to an agent with: mtix inbox --agent <id> --history\n", n)
}

// exportInboxNoteTo counts the unread mail and writes the export note to w.
func exportInboxNoteTo(ctx context.Context, w io.Writer) error {
	n, err := app.store.UnackedInboxEvents(ctx)
	if err != nil {
		return fmt.Errorf("count unacknowledged inbox events: %w", err)
	}
	writeExportInboxNote(w, n)
	return nil
}

// writeRecoverInboxNote reports the unread mail in the damaged database
// (opened read-only) that the recovered file cannot carry, or says the count
// could not be made.
func writeRecoverInboxNote(ctx context.Context, w io.Writer, dbPath string) {
	n, err := sqlite.CountUnackedInboxFile(ctx, dbPath)
	if err != nil {
		fmt.Fprintln(w, "note: unacknowledged inbox events in the damaged database could not be counted; "+
			"the recovered file carries tickets, not the inbox. Keep the damaged database file if any agent had unread mail.")
		return
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "note: the damaged database holds %d unacknowledged inbox event(s); the recovered file does not carry them "+
		"(the event journal is not part of tasks.json). Keep the damaged database file to read them.\n", n)
}
