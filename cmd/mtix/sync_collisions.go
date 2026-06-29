// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

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
human judgment (it may carry external references). No node is ever lost;
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
		Use:   "list [DSN]",
		Short: "List open restore collisions awaiting resolution",
		Long: `List unresolved restore collisions for the project. Each row surfaces
BOTH contesting nodes and their available signals (uids, epochs, claim
timestamps). The older-claim timestamp is ADVISORY only — it is
client-asserted and partly lost on restore, so it is shown, never acted on
automatically (audit F-5). --json for agent/CI consumption.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncCollisionsList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "",
		"Project prefix (defaults to the local project)")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	return cmd
}

func newSyncCollisionsResolveCmd() *cobra.Command {
	var (
		insecureTLS bool
		winner      string
	)
	cmd := &cobra.Command{
		Use:   "resolve <collision_id> [DSN]",
		Short: "Resolve a restore collision by choosing the winner",
		Long: `Resolve one restore collision (Option B). --winner selects which node
keeps the contested number:

  held      the create that currently holds the number on the hub keeps it
  incoming  the blocked (queued) create keeps it

The LOSER renumbers to the next free number under its parent via
Store.RenumberSubtree (no create event is deleted, no node is lost). The
moved node may have external references that need updating.`,
		Args: cobra.RangeArgs(1, 2),
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
		"Allow weaker TLS modes on loopback hosts (development only)")
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
	if app.jsonOutput {
		body, mErr := json.MarshalIndent(open, "", "  ")
		if mErr != nil {
			return mErr
		}
		_, err = fmt.Fprintln(stdout, string(body))
		return err
	}
	printCollisions(stdout, open)
	return nil
}

func runSyncCollisionsResolve(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, winner string,
) error {
	if winner != collisionWinnerHeld && winner != collisionWinnerIncoming {
		return fmt.Errorf("mtix sync collisions resolve: --winner must be one of: held, incoming")
	}
	collisionID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("mtix sync collisions resolve: collision_id must be an integer: %w", err)
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

	c, err := pool.GetOpenCollision(ctx, collisionID)
	if err != nil {
		return wrapSyncErr(stderr, "load collision", err)
	}
	if c.CollisionID == 0 {
		return fmt.Errorf("mtix sync collisions resolve: no open collision with id %d", collisionID)
	}

	// Pick winner/loser. The loser renumbers locally; no create is deleted.
	winnerEventID, loserUID := c.HeldEventID, c.IncomingUID
	if winner == collisionWinnerIncoming {
		winnerEventID, loserUID = c.IncomingEventID, c.HeldUID
	}

	// Renumber the loser's subtree to the next free number under its parent
	// (Store.RenumberSubtree via the ordinary renumber path, MTIX-30.5/30.7).
	// The loser node must exist in THIS client's local store — run resolve on
	// the client that owns the loser. No node is lost: the create is re-stamped
	// to the new path and re-queued, never deleted.
	loserNewPath, err := app.store.RenumberForHubRejection(ctx, loserUID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return fmt.Errorf(
				"mtix sync collisions resolve: the losing node (uid %s) is not in this local store; "+
					"run resolve on the client that owns it", loserUID)
		}
		return wrapSyncErr(stderr, "renumber loser", err)
	}

	resolved, err := pool.ResolveCollision(ctx, collisionID, winnerEventID, loserNewPath, syncActor())
	if err != nil {
		return wrapSyncErr(stderr, "resolve collision", err)
	}
	if !resolved {
		fmt.Fprintf(stderr,
			"warning: collision %d was already resolved by another operator; the local renumber to %s still stands\n",
			collisionID, loserNewPath)
		return nil
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
			shortHashForCLI(c.HeldUID), c.HeldEpoch, c.HeldWallClockTS,
			advisoryOlder(c.HeldWallClockTS, c.IncomingWallClockTS))
		fmt.Fprintf(w, "    incoming uid=%s epoch=%d claim_ts=%d  %s\n",
			shortHashForCLI(c.IncomingUID), c.DetectedEpoch, c.IncomingWallClockTS,
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
