// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Orphaned dependent events in a clone (MTIX-95.37.1). A hub that already
// holds events pushed under an old number by a client before 0.5.4 (the claim
// or child create of a creation the hub renumbered, MTIX-95.37) has events
// whose task this clone never creates. Applying one returns ErrNotFound. Pull
// quarantines such an event; the clone does the same, in the batch's
// transaction and the event's own savepoint, so the clone completes, the
// event is kept raw in sync_quarantine (source pull, reason starting with
// sqlite.CloneOrphanReason) and is retried by every pull. Every other apply
// failure still fails the clone.

// applyCloneEvent applies e in its own savepoint. quarantined reports that
// e was stored in the quarantine instead of applied: it was already there
// (a resumed clone: left as it is, nothing recorded twice), or its apply
// failed with ErrNotFound. Any other apply failure is returned.
func applyCloneEvent(ctx context.Context, tx *sql.Tx, in pullIngest, e *model.SyncEvent) (quarantined bool, err error) {
	already, err := sqlite.IsQuarantined(ctx, tx, e.EventID)
	if err != nil {
		return false, fmt.Errorf("check quarantine %s: %w", format.EventID(e.EventID), err)
	}
	if already {
		return true, nil
	}
	rejected, err := applyPulledEvent(ctx, tx, e, in.apply)
	if err != nil {
		return false, err
	}
	if rejected == nil {
		return false, nil
	}
	if !errors.Is(rejected, model.ErrNotFound) {
		return false, fmt.Errorf("apply %s: %w", format.EventID(e.EventID), rejected)
	}
	reason := fmt.Errorf("%s: %w", sqlite.CloneOrphanReason, rejected)
	if err := quarantinePulledEvent(ctx, tx, in, quarantineSourcePull, e, reason); err != nil {
		return false, err
	}
	if in.report != nil {
		fmt.Fprintf(in.report, "clone: quarantined event %s (%s on %s): its task does not exist in this clone; see 'mtix sync quarantine list'\n",
			format.EventID(e.EventID), e.OpType, format.EventID(e.NodeID))
	}
	return true, nil
}

// cloneCompleteLine is the line `mtix sync clone` prints on success: the
// events applied, the batches, and, when any, the orphaned events held in
// the quarantine with the next step.
func cloneCompleteLine(pulled, batches, orphans int) string {
	line := fmt.Sprintf("clone complete: %d events applied across %d batches", pulled, batches)
	if orphans > 0 {
		line += fmt.Sprintf("; %d orphaned events quarantined (their task was never created here); "+
			"run 'mtix sync quarantine list' to see them and 'mtix sync doctor' for the fix", orphans)
	}
	return line + "\n"
}

// countCloneOrphans returns how many orphaned events the clone quarantined.
func countCloneOrphans(ctx context.Context, st *sqlite.Store) (int, error) {
	n, err := st.CountQuarantinedCloneOrphans(ctx)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// finishClone prints the clone's completion line with the number of orphaned
// events it quarantined (MTIX-95.37.1).
func finishClone(ctx context.Context, stdout, stderr io.Writer, pulled, batches int) error {
	orphans, err := countCloneOrphans(ctx, app.store)
	if err != nil {
		return wrapSyncErr(stderr, "count orphans", err)
	}
	fmt.Fprint(stdout, cloneCompleteLine(pulled, batches, orphans))
	return nil
}
