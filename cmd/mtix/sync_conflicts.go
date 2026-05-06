// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ConflictRow is the JSON-serializable shape returned by `mtix sync
// conflicts list`. Mirrors the local sync_conflicts table.
type ConflictRow struct {
	ConflictID    int64  `json:"conflict_id"`
	EventIDWinner string `json:"event_id_winner"`
	EventIDLoser  string `json:"event_id_loser"`
	NodeID        string `json:"node_id"`
	FieldName     string `json:"field_name,omitempty"`
	Resolution    string `json:"resolution"`
	ResolvedAt    string `json:"resolved_at"`
	ResolvedBy    string `json:"resolved_by,omitempty"`
}

// validResolveActions are the FR-18.12 / SYNC-DESIGN section 11
// manual override choices.
var validResolveActions = map[string]bool{
	"keep-local":       true,
	"keep-remote":      true,
	"both-renumbered":  true,
	"acknowledge":      true,
}

// newSyncConflictsCmd creates the `mtix sync conflicts` command group
// with list and resolve subcommands.
func newSyncConflictsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List or resolve unresolved sync conflicts (FR-18.12)",
	}
	cmd.AddCommand(newSyncConflictsListCmd(), newSyncConflictsResolveCmd())
	return cmd
}

func newSyncConflictsListCmd() *cobra.Command {
	var (
		nodeFilter string
		batch      string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List unresolved sync conflicts",
		Long: `List rows from the local sync_conflicts table. Default output is a
human-readable table; --json for agent and CI consumption.

When unresolved conflicts exceed 50, a banner is printed pointing
at --batch <node_id> for batch resolution. --batch <node_id> filters
output to the named node.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if batch != "" {
				nodeFilter = batch
			}
			return runSyncConflictsList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), nodeFilter)
		},
	}
	cmd.Flags().StringVar(&nodeFilter, "node", "", "Filter by node ID")
	cmd.Flags().StringVar(&batch, "batch", "", "Group conflicts by node (alias for --node)")
	return cmd
}

func newSyncConflictsResolveCmd() *cobra.Command {
	var action string
	cmd := &cobra.Command{
		Use:   "resolve <conflict_id>",
		Short: "Manually resolve a sync conflict",
		Long: `Record a manual resolution decision for the given conflict_id.
--action must be one of: keep-local, keep-remote, both-renumbered,
acknowledge.

This command records the decision in sync_conflicts (a new row with
resolution='manual' since the original row is append-only per
FR-18.5). Actual state mutation (e.g. reverting a winner field) is
DEFERRED to a future ticket; v1 records the decision so audit history
is preserved and a follow-up tool can replay the choices.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncConflictsResolve(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args[0], action)
		},
	}
	cmd.Flags().StringVar(&action, "action", "",
		"Resolution action: keep-local | keep-remote | both-renumbered | acknowledge")
	if err := cmd.MarkFlagRequired("action"); err != nil {
		// MarkFlagRequired only fails if the flag doesn't exist —
		// here the flag is just declared, so this branch is unreachable.
		// Panicking is fine since this is wired during cmd construction.
		panic(err)
	}
	return cmd
}

func runSyncConflictsList(ctx context.Context, stdout, stderr io.Writer, nodeFilter string) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync conflicts list: not in an mtix project")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync conflicts list: local store not initialized")
	}
	rows, err := readConflicts(ctx, app.store, nodeFilter)
	if err != nil {
		return wrapSyncErr(stderr, "list conflicts", err)
	}
	if app.jsonOutput {
		body, mErr := json.MarshalIndent(rows, "", "  ")
		if mErr != nil {
			return mErr
		}
		_, err = fmt.Fprintln(stdout, string(body))
		return err
	}
	return printConflictsTable(stdout, rows)
}

func runSyncConflictsResolve(ctx context.Context, stdout, stderr io.Writer,
	conflictIDArg, action string,
) error {
	// Validate inputs before checking infrastructure so a typo in
	// --action surfaces a useful message even when the project isn't
	// fully initialized.
	if !validResolveActions[action] {
		return fmt.Errorf("mtix sync conflicts resolve: --action must be one of: keep-local, keep-remote, both-renumbered, acknowledge")
	}
	conflictID, err := strconv.ParseInt(conflictIDArg, 10, 64)
	if err != nil {
		return fmt.Errorf("mtix sync conflicts resolve: conflict_id must be an integer: %w", err)
	}
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync conflicts resolve: not in an mtix project")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync conflicts resolve: local store not initialized")
	}

	original, err := lookupConflict(ctx, app.store, conflictID)
	if err != nil {
		return wrapSyncErr(stderr, "lookup conflict", err)
	}
	if original.ConflictID == 0 {
		return fmt.Errorf("mtix sync conflicts resolve: conflict_id %d not found", conflictID)
	}

	if err := recordManualResolution(ctx, app.store, original, action); err != nil {
		return wrapSyncErr(stderr, "record resolution", err)
	}

	fmt.Fprintf(stdout,
		"recorded manual resolution for conflict %d on node %s (action=%s)\n",
		original.ConflictID, original.NodeID, action)
	return nil
}

func readConflicts(ctx context.Context, store *sqlite.Store, nodeFilter string) ([]ConflictRow, error) {
	q := `SELECT conflict_id, event_id_winner, event_id_loser, node_id,
	             COALESCE(field_name, ''), resolution, resolved_at,
	             COALESCE(resolved_by, '')
	      FROM sync_conflicts`
	args := []any{}
	if nodeFilter != "" {
		q += ` WHERE node_id = ?`
		args = append(args, nodeFilter)
	}
	q += ` ORDER BY conflict_id`
	rows, err := store.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ConflictRow
	for rows.Next() {
		var r ConflictRow
		if scanErr := rows.Scan(
			&r.ConflictID, &r.EventIDWinner, &r.EventIDLoser, &r.NodeID,
			&r.FieldName, &r.Resolution, &r.ResolvedAt, &r.ResolvedBy,
		); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func lookupConflict(ctx context.Context, store *sqlite.Store, id int64) (ConflictRow, error) {
	var r ConflictRow
	err := store.QueryRow(ctx, `
		SELECT conflict_id, event_id_winner, event_id_loser, node_id,
		       COALESCE(field_name, ''), resolution, resolved_at,
		       COALESCE(resolved_by, '')
		FROM sync_conflicts WHERE conflict_id = ?`, id,
	).Scan(
		&r.ConflictID, &r.EventIDWinner, &r.EventIDLoser, &r.NodeID,
		&r.FieldName, &r.Resolution, &r.ResolvedAt, &r.ResolvedBy,
	)
	if err == sql.ErrNoRows {
		return ConflictRow{}, nil
	}
	return r, err
}

// recordManualResolution appends a manual-resolution row to
// sync_conflicts. The original 'lww' row remains; the new row marks
// the user's choice. A future ticket can replay these choices to
// reconstruct the resolution timeline.
func recordManualResolution(ctx context.Context, store *sqlite.Store, original ConflictRow, action string) error {
	return store.WithTx(ctx, func(tx *sql.Tx) error {
		now := nowISO()
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sync_conflicts
			  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at, resolved_by)
			VALUES (?, ?, ?, ?, 'manual', ?, ?)`,
			original.EventIDWinner, original.EventIDLoser, original.NodeID,
			nullIfEmpty(original.FieldName), now, action,
		)
		return err
	})
}

// nowISO returns the current time in RFC3339Nano. Wall-clock; tests
// that care about determinism inject specific timestamps via the
// underlying store seam, not this helper.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func printConflictsTable(w io.Writer, rows []ConflictRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "no unresolved conflicts")
		return err
	}
	if len(rows) > 50 {
		fmt.Fprintf(w,
			"%d unresolved conflicts. Use 'mtix sync conflicts list --batch <node-id>' to scope.\n\n",
			len(rows))
	}
	for _, r := range rows {
		fmt.Fprintf(w, "[%d] %s field=%s resolution=%s winner=%s loser=%s\n",
			r.ConflictID, r.NodeID, emptyDash(r.FieldName), r.Resolution,
			shortHashForCLI(r.EventIDWinner), shortHashForCLI(r.EventIDLoser))
	}
	return nil
}
