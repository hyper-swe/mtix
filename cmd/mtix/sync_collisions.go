// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Admin resolution of RESTORE collisions (ADR-003 §6.1, Addendum A §15,
// Option B). A restore collision is the rare settled-vs-settled number contest
// that crossed an operator restore-bump; it is NEVER auto-resolved (which node
// keeps the number is a human judgment, audit F-5). These commands let an
// operator LIST open collisions and RESOLVE one by choosing the winner; the
// loser renumbers via Store.RenumberSubtree (MTIX-30.5). No node is ever lost.

// collisionWinnerHeld / collisionWinnerIncoming are the two winner choices.
const (
	collisionWinnerHeld     = "held"
	collisionWinnerIncoming = "incoming"
)

// newSyncCollisionsCmd creates the `mtix sync collisions` command group with
// list and resolve subcommands (ADR-003 §6.1).
func newSyncCollisionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collisions",
		Short: "List or resolve restore collisions (ADR-003 §6.1, Option B)",
		Long: `Manage restore collisions — settled-vs-settled number contests detected
across a hub restore boundary (see 'mtix sync mark-restored').

These are NOT auto-resolved: which node keeps the contested number is a
manual judgment (it may carry external references). No node is ever lost;
the loser renumbers to the next free number under its parent.`,
	}
	cmd.AddCommand(newSyncCollisionsListCmd(), newSyncCollisionsResolveCmd())
	return cmd
}

func newSyncCollisionsListCmd() *cobra.Command {
	var (
		insecureTLS bool
		project     string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List open restore collisions awaiting resolution",
		Long: `List unresolved restore collisions for the project. Each row surfaces
BOTH contesting nodes and their available signals (uids, epochs, claim
timestamps). The older-claim timestamp is ADVISORY only — it is
client-asserted and partly lost on restore, so it is shown, never acted on
automatically (audit F-5). --json for agent/CI consumption.`,
		Args: syncExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncCollisionsList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "",
		"Project prefix (defaults to the local project)")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	return cmd
}

func newSyncCollisionsResolveCmd() *cobra.Command {
	var (
		insecureTLS bool
		winner      string
	)
	cmd := &cobra.Command{
		Use:   "resolve <collision_id>",
		Short: "Resolve a restore collision by choosing the winner",
		Long: `Resolve one restore collision (Option B). --winner selects which node
keeps the contested number:

  held      the create that currently holds the number on the hub keeps it
  incoming  the blocked (queued) create keeps it

The hub accepts the choice first; only then does the LOSER renumber to the
next free number under its parent via Store.RenumberSubtree (no create event
is deleted, no node is lost). A hub that refuses the update leaves this local
store unchanged. If the local move fails after the hub accepted, the command
prints the exact command to run again on the client that owns the losing task;
a repeat is safe and a completed collision is a no-op. The moved node may have
external references that need updating.`,
		Args: syncExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncCollisionsResolve(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, winner)
		},
	}
	cmd.Flags().StringVar(&winner, "winner", "",
		"Which node keeps the number: held | incoming")
	if err := cmd.MarkFlagRequired("winner"); err != nil {
		panic(err) // unreachable: the flag is declared just above
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	return cmd
}

func runSyncCollisionsList(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, project string,
) error {
	if app.mtixDir == "" || app.store == nil {
		return fmt.Errorf("mtix sync collisions list: not in an mtix project (run 'mtix init' first)")
	}
	prefix, err := migrateProjectPrefix(ctx, project)
	if err != nil {
		return wrapSyncErr(stderr, "resolve project", err)
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

	open, err := pool.ListOpenCollisions(ctx, prefix)
	if err != nil {
		return wrapSyncErr(stderr, "list collisions", err)
	}
	pending, err := pool.ListPendingCollisionMoves(ctx, prefix)
	if err != nil {
		return wrapSyncErr(stderr, "list pending collision moves", err)
	}
	return writeCollisionList(stdout, open, pending, app.jsonOutput)
}

// collisionListEntry is one row of `collisions list --json`: an open collision
// (no extra fields, as before) or an accepted one whose local move is pending.
type collisionListEntry struct {
	transport.OpenCollision
	PendingLocalMove bool   `json:"pending_local_move,omitempty"`
	Winner           string `json:"winner,omitempty"`
}

// pendingWinnerChoice maps the recorded winning event to the --winner value
// that resumes the move ("" when it matches neither side).
func pendingWinnerChoice(m transport.PendingCollisionMove) string {
	switch m.WinnerEventID {
	case m.HeldEventID:
		return collisionWinnerHeld
	case m.IncomingEventID:
		return collisionWinnerIncoming
	}
	return ""
}

// writeCollisions lists open collisions only.
func writeCollisions(stdout io.Writer, open []transport.OpenCollision, jsonOutput bool) error {
	return writeCollisionList(stdout, open, nil, jsonOutput)
}

// writeCollisionList preserves exact ids in JSON and renders them safely in
// text. Accepted collisions whose local move is still pending are listed after
// the open ones, with the command that finishes them.
func writeCollisionList(stdout io.Writer, open []transport.OpenCollision, pending []transport.PendingCollisionMove, jsonOutput bool) error {
	if jsonOutput {
		var entries []collisionListEntry // nil stays null in JSON, as before
		for _, c := range open {
			entries = append(entries, collisionListEntry{OpenCollision: c})
		}
		for _, m := range pending {
			entries = append(entries, collisionListEntry{OpenCollision: m.OpenCollision, PendingLocalMove: true, Winner: pendingWinnerChoice(m)})
		}
		body, mErr := json.MarshalIndent(entries, "", "  ")
		if mErr != nil {
			return mErr
		}
		_, err := fmt.Fprintln(stdout, string(body))
		return err
	}
	printCollisions(stdout, open)
	printPendingCollisionMoves(stdout, pending)
	return nil
}

// printPendingCollisionMoves lists accepted collisions that wait for the local
// move, each with the exact command to run on the client that owns the loser.
func printPendingCollisionMoves(w io.Writer, pending []transport.PendingCollisionMove) {
	if len(pending) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%d accepted collision(s) waiting for the local move — run the command on the client that owns the losing task:\n", len(pending))
	for _, m := range pending {
		choice := pendingWinnerChoice(m)
		if choice == "" {
			choice = "held|incoming"
		}
		fmt.Fprintf(w, "[%d] %s: mtix sync collisions resolve %d --winner %s\n",
			m.CollisionID, format.EventID(m.DisplayPath), m.CollisionID, choice)
	}
}

func runSyncCollisionsResolve(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, winner string,
) error {
	if winner != collisionWinnerHeld && winner != collisionWinnerIncoming {
		return fmt.Errorf("mtix sync collisions resolve: --winner must be one of: held, incoming")
	}
	collisionID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		// Deliberately not wrapped: err quotes the argument, which may be
		// a DSN typed in the wrong place (FR-18.17, MTIX-95.15).
		return fmt.Errorf("mtix sync collisions resolve: collision_id must be an integer: %w", model.ErrInvalidInput)
	}
	if app.mtixDir == "" || app.store == nil {
		return fmt.Errorf("mtix sync collisions resolve: not in an mtix project (run 'mtix init' first)")
	}
	dsn, err := resolveSyncDSN(args[1:])
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}
	pool, err := transport.New(ctx, dsn, opts)
	if err != nil {
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	if err := resolveConnectedCollision(ctx, stdout, pool, collisionID, winner); err != nil {
		return wrapSyncErr(stderr, "resolve collision", err)
	}
	return nil
}

func resolveConnectedCollision(ctx context.Context, stdout io.Writer, hub collisionHub, collisionID int64, winner string) error {
	c, decision, completedPath, err := hub.GetCollision(ctx, collisionID)
	if err != nil {
		return fmt.Errorf("load collision: %w", err)
	}
	if c.CollisionID == 0 {
		return fmt.Errorf("mtix sync collisions resolve: no collision with id %d", collisionID)
	}

	// Pick winner/loser. The loser renumbers locally; no create is deleted.
	winnerEventID, loserUID := c.HeldEventID, c.IncomingUID
	if winner == collisionWinnerIncoming {
		winnerEventID, loserUID = c.IncomingEventID, c.HeldUID
	}

	if decision != "" && decision != winnerEventID {
		return fmt.Errorf("collision %d was resolved with a different winner; local store unchanged", collisionID)
	}
	if completedPath != "" {
		fmt.Fprintf(stdout, "collision %d already resolved; loser renumbered to %s (no-op)\n", collisionID, completedPath)
		return nil
	}
	// Check read-only that this client holds the loser BEFORE the hub row is
	// touched: a run on the wrong client must leave the collision open.
	if _, locateErr := locateCollisionLoser(ctx, loserUID); locateErr != nil {
		return locateErr
	}
	move := collisionMove{
		accept: func(ctx context.Context) error { return acceptCollision(ctx, hub, collisionID, winnerEventID) },
		renumber: func(ctx context.Context, uid string) (string, error) {
			return renumberCollisionLoser(ctx, uid, c.DisplayPath)
		},
		finish: func(ctx context.Context, path string) error {
			return hub.CompleteCollisionMove(ctx, collisionID, winnerEventID, path)
		},
	}
	loserNewPath, err := resolveCollisionMove(ctx, c, winner, loserUID, move)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout,
		"resolved collision %d on %s: winner keeps the number, loser renumbered to %s "+
			"(no node lost; update any external references to the moved node)\n",
		collisionID, c.DisplayPath, loserNewPath)
	return nil
}

// syncActor returns the local actor name for the resolution audit trail,
// falling back to "operator" when no machine identity is available.
func syncActor() string {
	if h := clientMachineHash(); h != "" {
		return h
	}
	return "operator"
}

// printCollisions renders the open collisions as a human-readable table. The
// older-claim hint is advisory only (audit F-5).
func printCollisions(w io.Writer, open []transport.OpenCollision) {
	if len(open) == 0 {
		fmt.Fprintln(w, "no open restore collisions")
		return
	}
	fmt.Fprintf(w, "%d open restore collision(s) — choose a winner with 'mtix sync collisions resolve <id> --winner held|incoming'\n\n",
		len(open))
	for _, c := range open {
		fmt.Fprintf(w, "[%d] %s (detected epoch %d)\n", c.CollisionID, c.DisplayPath, c.DetectedEpoch)
		fmt.Fprintf(w, "    held     uid=%s epoch=%d claim_ts=%d  %s\n",
			format.EventID(c.HeldUID), c.HeldEpoch, c.HeldWallClockTS,
			advisoryOlder(c.HeldWallClockTS, c.IncomingWallClockTS))
		fmt.Fprintf(w, "    incoming uid=%s epoch=%d claim_ts=%d  %s\n",
			format.EventID(c.IncomingUID), c.DetectedEpoch, c.IncomingWallClockTS,
			advisoryOlder(c.IncomingWallClockTS, c.HeldWallClockTS))
	}
}

// advisoryOlder labels the side with the earlier client-asserted claim
// timestamp. ADVISORY ONLY (audit F-5): timestamps are forgeable and partly
// lost on restore, so this is a hint for the human, never an auto-decision.
func advisoryOlder(mine, other int64) string {
	if mine < other {
		return "(older claim — advisory default)"
	}
	return ""
}
