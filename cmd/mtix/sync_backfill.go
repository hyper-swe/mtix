// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// newSyncBackfillCmd creates `mtix sync backfill` per MTIX-15.13.1.
//
// Synthesizes sync_events rows for every existing node, annotation,
// and dependency in the local SQLite. Used by v0.1.x → v0.2.0-beta
// upgraders so their pre-FR-18 history flows to the hub on the next
// `mtix sync push`.
func newSyncBackfillCmd() *cobra.Command {
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Synthesize sync_events from existing nodes (v0.1.x → v0.2.0-beta upgraders)",
		Long: `Walk the canonical nodes, annotations, and dependencies tables and emit
sync_events rows so the next 'mtix sync push' populates the hub with
the full project history.

Use this command ONCE per project, after upgrading from v0.1.x to
v0.2.0-beta. Existing teammates run 'mtix sync clone' against the
populated hub to replicate the history locally.

Safety properties:
  * Single-tx atomicity: all events OR none. SQLite WAL rolls back
    on any failure mid-walk (including SIGKILL).
  * Refusal-by-default if sync_events is non-empty. To re-backfill
    from scratch, run 'mtix sync reconcile --discard-local' first.
  * Refusal if the nodes table fails an FK invariant check
    (parent_id pointing at a missing parent). Run 'mtix verify' first.
  * Acquires the pushlock so a concurrent daemon push cannot race
    the emit path.
  * authorID for synthesized events defaults to 'cli' — same-authorID
    conflict logging caveat applies (see docs/SECURITY-MODEL.md).

After backfill: run 'mtix sync push' to ship events to the hub.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncBackfill(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				dryRun, force)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print counts without writing anything")
	cmd.Flags().BoolVar(&force, "force", false,
		"Re-backfill even if sync_events is non-empty (DANGEROUS — causes duplicate event_ids; hub dedupes by event_id so the dup is invisible there, but the local queue grows)")
	if err := cmd.Flags().MarkHidden("force"); err != nil {
		panic(err)
	}
	return cmd
}

func runSyncBackfill(ctx context.Context, stdout, stderr io.Writer, dryRun, force bool) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync backfill: not in an mtix project")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync backfill: local store not initialized")
	}

	// Acquire the pushlock to prevent concurrent push from racing the
	// emit walk. Skipped when --dry-run since dry-run is read-only.
	if !dryRun {
		lock, err := pushlock.Acquire(app.mtixDir)
		if errors.Is(err, pushlock.ErrLockHeld) {
			fmt.Fprintln(stderr,
				"mtix sync backfill: another sync operation is running (push/daemon); retry after it completes")
			return err
		}
		if err != nil {
			return wrapSyncErr(stderr, "pushlock", err)
		}
		defer func() { _ = lock.Release() }()
	}

	if dryRun {
		result, err := app.store.BackfillDryRun(ctx)
		if err != nil {
			return formatBackfillError(stderr, err)
		}
		printBackfillResult(stdout, result, true)
		return nil
	}

	result, err := app.store.Backfill(ctx, force)
	if err != nil {
		return formatBackfillError(stderr, err)
	}

	printBackfillResult(stdout, result, false)
	if !app.jsonOutput {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "Next: run 'mtix sync push' to ship events to the hub.")
		fmt.Fprintln(stdout, "Note: all backfilled events use authorID='cli'. Same-authorID")
		fmt.Fprintln(stdout, "concurrent edits do not produce hub-side sync_conflicts rows;")
		fmt.Fprintln(stdout, "LWW at apply time still converges replicas. See docs/SECURITY-MODEL.md.")
	}
	return nil
}

// formatBackfillError maps the sentinel error returns from store.Backfill
// into actionable CLI messages. The error itself flows through
// wrapSyncErr so the DSN redactor catches anything that slipped in (it
// won't, since backfill is local — but belt-and-suspenders).
func formatBackfillError(stderr io.Writer, err error) error {
	switch {
	case errors.Is(err, sqlite.ErrBackfillSyncEventsNonEmpty):
		return fmt.Errorf(
			"mtix sync backfill: sync_events table is non-empty. " +
				"If backfill was previously run, re-run 'mtix sync push' to drain " +
				"pending events. To re-backfill from scratch, run " +
				"'mtix sync reconcile --discard-local' first")
	case errors.Is(err, sqlite.ErrBackfillNodesInvariant):
		return fmt.Errorf(
			"mtix sync backfill: nodes table has invariant violations. " +
				"Run 'mtix verify' to investigate; refusing to propagate " +
				"corrupt data to the hub")
	default:
		return wrapSyncErr(stderr, "backfill", err)
	}
}

// printBackfillResult renders a structured summary or JSON.
func printBackfillResult(stdout io.Writer, result sqlite.BackfillResult, dryRun bool) {
	if app.jsonOutput {
		body, _ := json.MarshalIndent(map[string]any{
			"dry_run": dryRun,
			"result":  result,
		}, "", "  ")
		fmt.Fprintln(stdout, string(body))
		return
	}
	prefix := "backfill complete:"
	if dryRun {
		prefix = "backfill dry-run:"
	}
	fmt.Fprintf(stdout,
		"%s %d nodes walked → %d events emitted (%d create_node, %d update_field, %d transition, %d annotate, %d link_dep)\n",
		prefix,
		result.NodeCount,
		result.TotalEvents,
		result.CreateEvents,
		result.UpdateFieldEvents,
		result.TransitionEvents,
		result.AnnotateEvents,
		result.LinkDepEvents,
	)
}
