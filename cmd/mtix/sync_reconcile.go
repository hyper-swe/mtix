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

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newSyncReconcileCmd creates `mtix sync reconcile` per FR-18.13. The
// command wraps the data-layer functions from MTIX-15.6 — DiscardLocal,
// RenameTo, ImportAs — with a CLI surface plus the --dry-run preview.
//
// Exactly one of --discard-local / --rename-to / --import-as is required.
// Destructive actions require --yes (or default to --dry-run output).
func newSyncReconcileCmd() *cobra.Command {
	var (
		discardLocal bool
		renameTo     string
		importAs     string
		dryRun       bool
		yes          bool
	)
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Resolve divergent history (FR-18.13)",
		Long: `Resolve a divergent-history error by choosing one of the four paths:

  --discard-local            drop local nodes/events and take hub state
  --rename-to NEWPREFIX      rewrite local IDs to a new prefix
  --import-as PARENT-ID      re-parent local tree under PARENT-ID
  --dry-run                  preview the chosen path without mutating

Exactly one path flag must be set. --dry-run is implicit unless --yes
is also set; without --yes the command prints the Plan (renames,
node count) and exits without mutation. With --yes, executes the path.

See 'mtix sync init' for divergent-history detection.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSyncReconcile(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				reconcileFlags{
					discardLocal: discardLocal,
					renameTo:     renameTo,
					importAs:     importAs,
					dryRun:       dryRun,
					yes:          yes,
				})
		},
	}
	cmd.Flags().BoolVar(&discardLocal, "discard-local", false, "Drop local state, take hub state")
	cmd.Flags().StringVar(&renameTo, "rename-to", "", "Rewrite local IDs to NEWPREFIX")
	cmd.Flags().StringVar(&importAs, "import-as", "", "Re-parent local tree under PARENT-ID")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview the plan without mutation")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm destructive action; required for non-dry-run")
	return cmd
}

type reconcileFlags struct {
	discardLocal bool
	renameTo     string
	importAs     string
	dryRun       bool
	yes          bool
}

func (f reconcileFlags) pathCount() int {
	n := 0
	if f.discardLocal {
		n++
	}
	if f.renameTo != "" {
		n++
	}
	if f.importAs != "" {
		n++
	}
	return n
}

func runSyncReconcile(ctx context.Context, stdout, stderr io.Writer, f reconcileFlags) error {
	// Validate flag combination before infrastructure so a misuse of
	// the path flags surfaces immediately even outside an initialized
	// project (matches mtix sync conflicts resolve's ordering).
	if f.pathCount() != 1 {
		return fmt.Errorf("mtix sync reconcile: exactly one of --discard-local, --rename-to, --import-as is required")
	}
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync reconcile: not in an mtix project")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync reconcile: local store not initialized")
	}

	// Default-to-dry-run safety: without --yes, treat as preview.
	effectiveDry := f.dryRun || !f.yes

	if effectiveDry {
		return runReconcileDryRun(ctx, stdout, stderr, f)
	}
	return runReconcileExecute(ctx, stdout, stderr, f)
}

func runReconcileDryRun(ctx context.Context, stdout, stderr io.Writer, f reconcileFlags) error {
	plan, err := computePlan(ctx, app.store, f)
	if err != nil {
		return wrapSyncErr(stderr, "dry-run", err)
	}
	if app.jsonOutput {
		body, mErr := json.MarshalIndent(plan, "", "  ")
		if mErr != nil {
			return mErr
		}
		_, err = fmt.Fprintln(stdout, string(body))
		return err
	}
	printReconcilePlan(stdout, plan, !f.yes)
	return nil
}

func computePlan(ctx context.Context, store *sqlite.Store, f reconcileFlags) (sqlite.Plan, error) {
	switch {
	case f.discardLocal:
		return sqlite.DryRunDiscardLocal(ctx, store)
	case f.renameTo != "":
		return sqlite.DryRunRenameTo(ctx, store, f.renameTo)
	case f.importAs != "":
		return sqlite.DryRunImportAs(ctx, store, f.importAs)
	}
	return sqlite.Plan{}, fmt.Errorf("no path selected")
}

func runReconcileExecute(ctx context.Context, stdout, stderr io.Writer, f reconcileFlags) error {
	switch {
	case f.discardLocal:
		if err := sqlite.DiscardLocal(ctx, app.store, app.mtixDir); err != nil {
			return wrapSyncErr(stderr, "discard-local", err)
		}
		fmt.Fprintln(stdout, "discard-local complete")
		return nil
	case f.renameTo != "":
		count, err := sqlite.RenameTo(ctx, app.store, app.mtixDir, f.renameTo)
		if err != nil {
			return wrapSyncErr(stderr, "rename-to", err)
		}
		fmt.Fprintf(stdout, "rename-to %s complete: %d nodes renamed\n", f.renameTo, count)
		return nil
	case f.importAs != "":
		count, err := sqlite.ImportAs(ctx, app.store, app.mtixDir, f.importAs)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return fmt.Errorf("mtix sync reconcile: parent %s not in local store; run 'mtix sync clone' first", f.importAs)
			}
			return wrapSyncErr(stderr, "import-as", err)
		}
		fmt.Fprintf(stdout, "import-as %s complete: %d nodes re-parented\n", f.importAs, count)
		return nil
	}
	return fmt.Errorf("no path selected")
}

func printReconcilePlan(w io.Writer, plan sqlite.Plan, autoDryRun bool) {
	if autoDryRun {
		fmt.Fprintln(w, "DRY RUN (re-run with --yes to execute)")
	}
	fmt.Fprintf(w, "path: %s\n", plan.Path)
	if plan.NewPrefix != "" {
		fmt.Fprintf(w, "new_prefix: %s\n", plan.NewPrefix)
	}
	if plan.ParentID != "" {
		fmt.Fprintf(w, "parent_id: %s\n", plan.ParentID)
	}
	fmt.Fprintf(w, "node_count: %d\n", plan.NodeCount)
	if len(plan.Renames) == 0 {
		return
	}
	fmt.Fprintln(w, "renames:")
	for _, r := range plan.Renames {
		fmt.Fprintf(w, "  %s -> %s\n", r.OldID, r.NewID)
	}
}
