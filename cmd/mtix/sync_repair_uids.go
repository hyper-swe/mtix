// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// newSyncRepairUIDsCmd creates `mtix sync repair-uids` (MTIX-92): the
// one-time upgrade step for a hub that received pushes from a client
// older than the MTIX-91 fix, which never sent uid.
//
// Why it exists: the hub registers a create under its stored uid, or its
// event_id when the uid is NULL. Every event pushed before the fix sits
// on the hub with uid NULL, so a fixed client re-emitting a create with
// the node's real uid (a regenerated backfill, for instance) looks like a
// DIFFERENT node claiming a taken number, and the push dies on a renumber
// it cannot settle. Stamping each hub create with its node's uid makes
// that re-push the no-op it was designed to be.
//
// It lives beside `mtix sync doctor` rather than inside it: doctor is
// read-only and exits by check status, and a repair that writes to a
// shared hub deserves its own name, its own --dry-run and its own report.
func newSyncRepairUIDsCmd() *cobra.Command {
	var (
		insecureTLS bool
		dryRun      bool
		project     string
	)
	cmd := &cobra.Command{
		Use:   "repair-uids [DSN]",
		Short: "Stamp hub create rows with their node uid (upgrade step for pre-MTIX-91 pushes)",
		Long: `Stamp every create_node row on the hub whose uid is NULL with the uid of
the matching local node, matched on (project, node id).

Run this ONCE per project, from a client that holds the local store, if
any push to the hub was made by a client older than the MTIX-91 fix
(v0.5.3-beta and earlier). Until then the hub knows those nodes only by
their original event id, and a fixed client that re-emits a create for
one of them — a regenerated backfill, a --force re-backfill — collides
with its own node and the push fails on a renumber it cannot settle.

Safety properties:
  * Nothing is deleted and a populated uid is never overwritten: the
    UPDATE is guarded by uid IS NULL. Re-running stamps nothing.
  * --dry-run reports what would be stamped and writes nothing.
  * Every project in the local store is repaired unless --project
    narrows it. Each project is reported separately.
  * Create rows on the hub for nodes this store does not hold are
    listed, not skipped silently: they stay unrepaired until the
    command runs from a client that has them.
  * A hub uid that is populated but DIFFERENT from the local one is
    listed as a mismatch and left alone — the hub and this store
    disagree about which logical node holds that number.

Local node uids are backfilled first (the same idempotent step as
'mtix sync migrate' Phase 0), so a store predating uids can still
repair its hub.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncRepairUIDs(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, project, dryRun)
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Report what would be stamped without writing to the hub")
	cmd.Flags().StringVar(&project, "project", "",
		"Repair only this project prefix (default: every project in the local store)")
	return cmd
}

// uidRepairHub is the one hub operation the command drives, narrowed to
// an interface so the reporting is unit-testable against a fake hub.
type uidRepairHub interface {
	RepairCreateUIDs(ctx context.Context, project string, localUIDs map[string]string, dryRun bool) (transport.UIDRepairReport, error)
}

func runSyncRepairUIDs(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, project string, dryRun bool,
) error {
	if app.mtixDir == "" || app.store == nil {
		return fmt.Errorf("mtix sync repair-uids: not in an mtix project (run 'mtix init' first)")
	}

	// Local, idempotent: a node with no uid cannot repair anything.
	if err := app.store.BackfillUIDs(ctx); err != nil {
		return wrapSyncErr(stderr, "backfill local uids", err)
	}
	uids, err := app.store.NodeUIDsByProject(ctx)
	if err != nil {
		return wrapSyncErr(stderr, "read local uids", err)
	}
	if project != "" {
		if uids[project] == nil {
			return fmt.Errorf("mtix sync repair-uids: no local nodes for project %q", project)
		}
		uids = map[string]map[string]string{project: uids[project]}
	}
	if len(uids) == 0 {
		fmt.Fprintln(stdout, "no local nodes; nothing to repair")
		return nil
	}

	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}
	pool, err := transport.New(ctx, dsn, opts)
	if err != nil {
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	reports, err := repairUIDsForProjects(ctx, pool, uids, dryRun)
	if err != nil {
		return wrapSyncErr(stderr, "repair", err)
	}
	if app.jsonOutput {
		body, _ := json.MarshalIndent(reports, "", "  ")
		fmt.Fprintln(stdout, string(body))
		return nil
	}
	printUIDRepairReports(stdout, stderr, reports)
	return nil
}

// repairUIDsForProjects runs the repair for every project in uids, in
// prefix order so the report is stable.
func repairUIDsForProjects(ctx context.Context, hub uidRepairHub,
	uids map[string]map[string]string, dryRun bool,
) ([]transport.UIDRepairReport, error) {
	prefixes := make([]string, 0, len(uids))
	for p := range uids {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	reports := make([]transport.UIDRepairReport, 0, len(prefixes))
	for _, p := range prefixes {
		r, err := hub.RepairCreateUIDs(ctx, p, uids[p], dryRun)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", p, err)
		}
		reports = append(reports, r)
	}
	return reports, nil
}

// printUIDRepairReports renders one line per project on stdout and the
// partial-repair details (hub-only nodes, mismatches) on stderr, where
// they are not lost in a pipeline.
func printUIDRepairReports(stdout, stderr io.Writer, reports []transport.UIDRepairReport) {
	for _, r := range reports {
		verb := "stamped"
		if r.DryRun {
			verb = "would stamp"
		}
		fmt.Fprintf(stdout, "%s: %s %d hub create row(s); %d already stamped, %d mismatched, %d hub-only\n",
			r.Project, verb, r.Stamped, r.AlreadyStamped, len(r.Mismatched), len(r.HubOnly))
		if len(r.HubOnly) > 0 {
			fmt.Fprintf(stderr, "%s: %d create row(s) on the hub have no local node and stay unstamped: %s\n"+
				"  run 'mtix sync repair-uids --project %s' from a client that holds them\n",
				r.Project, len(r.HubOnly), strings.Join(r.HubOnly, ", "), r.Project)
		}
		for _, m := range r.Mismatched {
			fmt.Fprintf(stderr, "%s: %s carries uid %s on the hub but %s locally; left untouched\n",
				r.Project, m.NodeID, m.HubUID, m.LocalUID)
		}
	}
	if len(reports) > 0 && reports[0].DryRun {
		fmt.Fprintln(stdout, "DRY RUN: nothing was written. Re-run without --dry-run to apply.")
	}
}
