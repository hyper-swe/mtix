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

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ConflictRow is the JSON-serializable shape returned by `mtix sync
// conflicts list`. Mirrors the local sync_conflicts table, plus whether
// the row is an unresolved conflict (MTIX-95.7).
type ConflictRow struct {
	ConflictID    int64  `json:"conflict_id"`
	EventIDWinner string `json:"event_id_winner"`
	EventIDLoser  string `json:"event_id_loser"`
	NodeID        string `json:"node_id"`
	FieldName     string `json:"field_name,omitempty"`
	Resolution    string `json:"resolution"`
	ResolvedAt    string `json:"resolved_at"`
	ResolvedBy    string `json:"resolved_by,omitempty"`
	Unresolved    bool   `json:"unresolved"`
}

// highConflictThreshold is the FR-18.12 banner threshold: above this many
// unresolved conflicts, status and list point at --batch.
const highConflictThreshold = 50

// resolveNoStateChange is what mtix sync conflicts resolve reports in text
// and --json: it records a decision and changes no node (MTIX-95.7,
// Commandment 11).
const resolveNoStateChange = "decision recorded; node state not changed"

// ResolveResult is the --json shape of `mtix sync conflicts resolve`: the
// conflict, the recorded action, and the statement that no node state
// changed (MTIX-95.7).
type ResolveResult struct {
	ConflictID       int64  `json:"conflict_id"`
	NodeID           string `json:"node_id"`
	FieldName        string `json:"field_name,omitempty"`
	Action           string `json:"action"`
	DecisionRecorded bool   `json:"decision_recorded"`
	NodeStateChanged bool   `json:"node_state_changed"`
	Message          string `json:"message"`
}

// validResolveActions are the FR-18.12 / SYNC-DESIGN section 11
// manual override choices.
var validResolveActions = map[string]bool{
	"keep-local":      true,
	"keep-remote":     true,
	"both-renumbered": true,
	"acknowledge":     true,
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

// newSyncConflictsListCmd creates `mtix sync conflicts list`: unresolved
// conflicts by default, every row with --all (FR-18.12, MTIX-95.7).
func newSyncConflictsListCmd() *cobra.Command {
	var (
		nodeFilter string
		batch      string
		all        bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List unresolved sync conflicts",
		Long: `List conflicts from the local sync_conflicts table. By default only the
unresolved ones: lww conflicts with no later manual resolution for the
same node and field ('mtix sync conflicts resolve' records one). A later
lww conflict on the same node and field is unresolved again.

--all lists every row, manual resolutions and tombstone rows included,
each marked unresolved=true or unresolved=false. Default output is a
human-readable table; --json for agent and CI consumption (each row
carries "unresolved").

When unresolved conflicts exceed 50, a banner is printed pointing
at --batch <node_id> for batch resolution. --batch <node_id> filters
output to the named node.`,
		Args: syncExactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if batch != "" {
				nodeFilter = batch
			}
			return runSyncConflictsList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), nodeFilter, all)
		},
	}
	cmd.Flags().StringVar(&nodeFilter, "node", "", "Filter by node ID")
	cmd.Flags().StringVar(&batch, "batch", "", "Group conflicts by node (alias for --node)")
	cmd.Flags().BoolVar(&all, "all", false, "List every row, resolved conflicts and manual resolutions included")
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

This command records the decision only: it appends a row with
resolution='manual' to sync_conflicts (the original row is append-only
per FR-18.5), and it changes no node. The output says so in text and
--json ("decision recorded; node state not changed"). To apply the value
you chose, edit the node with 'mtix update' and push.

The decision resolves the conflict and every earlier conflict on the same
node and field; a later conflict on that node and field is unresolved
again. A conflict_id that is itself a manual resolution is refused as
invalid input.`,
		Args: syncExactArgs(1),
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

// runSyncConflictsList prints the unresolved conflicts, or with all every
// sync_conflicts row, each marked unresolved or not (FR-18.12,
// MTIX-95.7). The unresolved rule is readConflicts', the same one sync
// status counts with.
func runSyncConflictsList(ctx context.Context, stdout, stderr io.Writer, nodeFilter string, all bool) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync conflicts list: not in an mtix project")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync conflicts list: local store not initialized")
	}
	rows, err := readConflicts(ctx, app.store, nodeFilter, all)
	if err != nil {
		return wrapSyncErr(stderr, "list conflicts", err)
	}
	if app.jsonOutput {
		body, mErr := json.MarshalIndent(rows, "", "  ")
		if mErr != nil {
			return fmt.Errorf("mtix sync conflicts list: %w", mErr)
		}
		_, err = fmt.Fprintln(stdout, string(body))
		return err
	}
	return printConflictsTable(stdout, rows, all)
}

// runSyncConflictsResolve records a manual decision for the conflict
// conflictIDArg as a new 'manual' row and reports, in text and --json,
// that no node state changed (FR-18.12, MTIX-95.7, Commandment 11). A
// conflict_id that is itself a manual row is invalid input.
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
		// Deliberately not wrapped: err quotes the argument, which may be
		// a DSN typed in the wrong place (FR-18.17, MTIX-95.15).
		return fmt.Errorf("mtix sync conflicts resolve: conflict_id must be an integer: %w",
			model.ErrInvalidInput)
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
	if original.Resolution == "manual" {
		return fmt.Errorf("mtix sync conflicts resolve: conflict_id %d is a manual resolution, not a conflict; "+
			"resolve the conflict it answers (see mtix sync conflicts list --all): %w",
			conflictID, model.ErrInvalidInput)
	}

	if err := recordManualResolution(ctx, app.store, original, action); err != nil {
		return wrapSyncErr(stderr, "record resolution", err)
	}
	return printResolveResult(stdout, original, action)
}

// printResolveResult reports a recorded decision, stating in text and
// --json that no node state changed: resolve records the choice only
// (MTIX-95.7, Commandment 11).
func printResolveResult(w io.Writer, original ConflictRow, action string) error {
	if app.jsonOutput {
		body, err := json.MarshalIndent(ResolveResult{
			ConflictID: original.ConflictID, NodeID: original.NodeID, FieldName: original.FieldName,
			Action: action, DecisionRecorded: true, NodeStateChanged: false, Message: resolveNoStateChange,
		}, "", "  ")
		if err != nil {
			return fmt.Errorf("mtix sync conflicts resolve: %w", err)
		}
		_, err = fmt.Fprintln(w, string(body))
		return err
	}
	_, err := fmt.Fprintf(w, "recorded manual resolution for conflict %d on node %s (action=%s)\n%s\n",
		original.ConflictID, original.NodeID, action, resolveNoStateChange)
	return err
}

// readConflicts returns the sync_conflicts rows of nodeFilter (every node
// when empty), in conflict_id order: only the unresolved ones, or every
// row when all is set. This is the one place the unresolved rule lives;
// sync status counts the rows it returns (MTIX-95.7).
func readConflicts(ctx context.Context, store *sqlite.Store, nodeFilter string, all bool) ([]ConflictRow, error) {
	// Each row with its unresolved flag: an lww row is unresolved until a
	// manual row for the same node and field (NULL fields pair with each
	// other) follows it in conflict_id order; a later lww row for the pair
	// is unresolved again. Tombstone and manual rows are never unresolved.
	// The two parameters filter by node ('' for every node) and choose
	// every row or only the unresolved ones.
	rows, err := store.Query(ctx, `
		SELECT conflict_id, event_id_winner, event_id_loser, node_id,
		       field_name, resolution, resolved_at, resolved_by, unresolved
		FROM (
		    SELECT c.conflict_id, c.event_id_winner, c.event_id_loser, c.node_id,
		           COALESCE(c.field_name, '') AS field_name, c.resolution, c.resolved_at,
		           COALESCE(c.resolved_by, '') AS resolved_by,
		           (c.resolution = 'lww' AND NOT EXISTS (
		               SELECT 1 FROM sync_conflicts m
		               WHERE m.resolution = 'manual'
		                 AND m.node_id = c.node_id
		                 AND m.field_name IS c.field_name
		                 AND m.conflict_id > c.conflict_id)) AS unresolved
		    FROM sync_conflicts c
		    WHERE (?1 = '' OR c.node_id = ?1)
		)
		WHERE ?2 OR unresolved
		ORDER BY conflict_id`, nodeFilter, all)
	if err != nil {
		return nil, fmt.Errorf("read conflicts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ConflictRow
	for rows.Next() {
		var r ConflictRow
		if scanErr := rows.Scan(
			&r.ConflictID, &r.EventIDWinner, &r.EventIDLoser, &r.NodeID,
			&r.FieldName, &r.Resolution, &r.ResolvedAt, &r.ResolvedBy, &r.Unresolved,
		); scanErr != nil {
			return nil, fmt.Errorf("read conflicts: %w", scanErr)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read conflicts: %w", err)
	}
	return out, nil
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

// printConflictsTable prints one line per row, marked unresolved or not.
// Its banner counts the unresolved rows only, whether or not all rows
// are listed (FR-18.12, MTIX-95.7).
func printConflictsTable(w io.Writer, rows []ConflictRow, all bool) error {
	if len(rows) == 0 {
		msg := "no unresolved conflicts"
		if all {
			msg = "no conflicts recorded"
		}
		_, err := fmt.Fprintln(w, msg)
		return err
	}
	unresolved := 0
	for _, r := range rows {
		if r.Unresolved {
			unresolved++
		}
	}
	if unresolved > highConflictThreshold {
		fmt.Fprintf(w,
			"%d unresolved conflicts. Use 'mtix sync conflicts list --batch <node-id>' to scope.\n\n",
			unresolved)
	}
	for _, r := range rows {
		fmt.Fprintf(w, "[%d] %s field=%s resolution=%s unresolved=%t winner=%s loser=%s\n",
			r.ConflictID, r.NodeID, emptyDash(r.FieldName), r.Resolution, r.Unresolved,
			shortHashForCLI(r.EventIDWinner), shortHashForCLI(r.EventIDLoser))
	}
	return nil
}
