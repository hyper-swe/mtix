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

// writeExportInboxNote tells the operator, on stderr, that a mirror export does not
// carry the inbox. The journal and the acks are per store: tasks.json holds
// tickets and annotations, so an import into another store starts with an
// empty inbox.
func writeExportInboxNote(w io.Writer, n int64) {
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "note: this store holds %d unacknowledged inbox event(s); this journal-free export does not carry them "+
		"(tasks.json is journal-free; export to a private file without --board-only to carry addressed mail). "+
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
// (opened read-only). The salvage report identifies what was recoverable.
func writeRecoverInboxNote(ctx context.Context, w io.Writer, dbPath string) {
	n, err := sqlite.CountUnackedInboxFile(ctx, dbPath)
	if err != nil {
		fmt.Fprintln(w, "note: unacknowledged inbox events in the damaged database could not be counted; "+
			"read the salvage report for retained and unavailable mail. Keep the damaged database file if recovery is incomplete.")
		return
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "note: the damaged database holds %d unacknowledged inbox event(s); read the salvage report for retained and unavailable mail. "+
		"Ordinary tasks.json does not carry the journal; keep the damaged database if recovery is incomplete.\n", n)
}
