// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
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

// applyCloneEvent applies e in its own savepoint. newlyQuarantined reports
// that this call stored e in the quarantine as an orphan: its apply failed
// with ErrNotFound, it is a creation whose parent task is absent, or it
// waits behind a quarantined creation of its task. An
// event already quarantined (an interrupted clone resumed) is left as it is
// and is not reported again. Any other apply failure is returned.
func applyCloneEvent(ctx context.Context, tx *sql.Tx, in pullIngest, e *model.SyncEvent) (newlyQuarantined bool, err error) {
	already, err := sqlite.IsQuarantined(ctx, tx, e.EventID)
	if err != nil {
		return false, fmt.Errorf("check quarantine %s: %w", format.EventID(e.EventID), err)
	}
	if already {
		return false, nil
	}
	// An event about a task whose creation is quarantined waits behind it,
	// as in a pull: applied first it could be a no-op (a delete) that the
	// retry of the creation would later undo (MTIX-95.37).
	behind, err := refuseBehindQuarantinedCreate(ctx, tx, e)
	if err != nil {
		return false, err
	}
	if behind != nil {
		return true, quarantineCloneOrphan(ctx, tx, in, e, behind)
	}
	rejected, err := applyPulledEvent(ctx, tx, e, in.apply)
	if err != nil {
		return false, err
	}
	if rejected == nil {
		return false, nil
	}
	orphan, err := isOrphanFailure(ctx, tx, e, rejected)
	if err != nil {
		return false, err
	}
	if !orphan {
		return false, fmt.Errorf("apply %s: %w", format.EventID(e.EventID), rejected)
	}
	return true, quarantineCloneOrphan(ctx, tx, in, e, rejected)
}

// quarantineCloneOrphan stores e in the quarantine as source pull with a
// reason that starts with sqlite.CloneOrphanReason, the clone-origin marker.
func quarantineCloneOrphan(ctx context.Context, tx *sql.Tx, in pullIngest, e *model.SyncEvent, cause error) error {
	reason := fmt.Errorf("%s: %w", sqlite.CloneOrphanReason, cause)
	return quarantinePulledEvent(ctx, tx, in, quarantineSourcePull, e, reason)
}

// isOrphanFailure reports whether rejected, the failed apply of e, means the
// event depends on a task this clone never creates: the apply could not find
// its node (ErrNotFound), or e creates a task whose parent is absent (the
// insert then fails its foreign key, a child create pushed under an old
// number). Called after the savepoint undid the apply's writes.
func isOrphanFailure(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, rejected error) (bool, error) {
	if errors.Is(rejected, model.ErrNotFound) {
		return true, nil
	}
	if e.OpType != model.OpCreateNode {
		return false, nil
	}
	var p model.CreateNodePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil || p.ParentID == "" {
		return false, nil
	}
	var n int
	// Whether the parent task the creation names exists here.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes WHERE id = ?`, p.ParentID).Scan(&n); err != nil {
		return false, fmt.Errorf("check parent of %s: %w", format.EventID(e.EventID), err)
	}
	return n == 0, nil
}

// reportCloneOrphans names, on w, each event a committed batch quarantined.
func reportCloneOrphans(w io.Writer, orphans []*model.SyncEvent) {
	for _, e := range orphans {
		fmt.Fprintf(w, "clone: quarantined event %s (%s on %s): its task does not exist in this clone; see 'mtix sync quarantine list'\n",
			format.EventID(e.EventID), e.OpType, format.EventID(e.NodeID))
	}
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

// resetCloneState resets the pull state a clone rebuilds. A fresh clone (the
// start of the log) clears the whole quarantine. A resumed one, which starts
// after its saved checkpoint, keeps the clone-origin orphans the committed
// batches quarantined, since it never re-reads those events, and clears the
// rest (MTIX-95.37.1).
func resetCloneState(ctx context.Context, st *sqlite.Store, since transport.PullCursor) error {
	if since == (transport.PullCursor{}) {
		return resetPullState(ctx, st)
	}
	if err := resetLateEventSweep(ctx, st); err != nil {
		return err
	}
	return st.ClearQuarantineExceptCloneOrphans(ctx)
}
