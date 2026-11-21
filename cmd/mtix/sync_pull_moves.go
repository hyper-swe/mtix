// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pullMoveMark returns the position of the end of the task history before a
// pull, for printPullMoves. A failure is only a warning: the pull goes on and
// the moves are then not listed (MTIX-112.17.16.39).
func pullMoveMark(ctx context.Context, stderr io.Writer, st *sqlite.Store) (mark int64, ok bool) {
	mark, err := st.PullMoveMark(ctx)
	if err != nil {
		warnSync(stderr, "mtix sync pull", err)
		return 0, false
	}
	return mark, true
}

// reportPullMoves reports, at the end of a pull, each local task the pull moved
// to another number because a hub task took its number (MTIX-112.17.16.39):
// one line on stdout (old number, new number, title), so an agent that still
// holds the old number does not act on the teammate's task, and the moves to
// req.onMoves when set (the daemon logs them, its stdout is discarded).
func reportPullMoves(ctx context.Context, req syncPullInput, st *sqlite.Store, mark int64) {
	moves, err := st.PullMovesSince(ctx, mark)
	if err != nil {
		warnSync(req.stderr, "mtix sync pull", err)
		return
	}
	for _, m := range moves {
		fmt.Fprintf(req.stdout, "renumbered: %s -> %s %s (your task moved because a hub task took %s; use the new number)\n",
			m.OldID, m.NewID, oneLine(m.Title), m.OldID)
	}
	if req.onMoves != nil && len(moves) > 0 {
		req.onMoves(moves)
	}
}

// logPullMoves returns the onMoves hook of a daemon pull: one log line per
// moved task, with the old and new number and the task's uid, because a daemon
// pull prints nothing (MTIX-112.17.16.39). A nil logger logs nothing.
func logPullMoves(logger *slog.Logger) func([]sqlite.PullMove) {
	return func(moves []sqlite.PullMove) {
		if logger == nil {
			return
		}
		for _, m := range moves {
			logger.Info("sync daemon pull renumbered a local task: a hub task took its number",
				"old_number", oneLine(m.OldID), "new_number", oneLine(m.NewID), "uid", oneLine(m.UID))
		}
	}
}
