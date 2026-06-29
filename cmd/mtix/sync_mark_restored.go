// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// newSyncMarkRestoredCmd creates `mtix sync mark-restored` — the OPERATOR's
// restore-from-backup runbook step that advances the hub restore-epoch
// (ADR-003 §15, Addendum A).
//
// RUNBOOK (restore the hub from a backup):
//  1. Restore the mtix-owned tables from a `mtix sync backup` dump.
//  2. Run `mtix sync mark-restored`. This advances the hub restore_epoch so
//     every surviving create's stamp falls into an EARLIER epoch than every
//     create accepted afterward — opening the restore window in which a
//     cross-epoch settled-vs-settled collision is detected as Option B
//     (§6.1) instead of being silently renumbered (§6).
//  3. Let clients reconnect and push. Any genuine restore collision surfaces
//     via `mtix sync collisions list` for human resolution.
//
// This is the ONLY way to advance the epoch: no client/push path touches it,
// so a compromised client cannot manufacture a restore window (§15). Run it
// exactly once per restore.
func newSyncMarkRestoredCmd() *cobra.Command {
	var insecureTLS bool
	cmd := &cobra.Command{
		Use:   "mark-restored [DSN]",
		Short: "Operator: advance the hub restore-epoch after a backup restore (ADR-003 §15)",
		Long: `Advance the hub restore-epoch by one. Run this EXACTLY ONCE immediately
after restoring the hub from a backup.

Advancing the epoch opens a restore window: a settled-vs-settled number
collision whose held create predates this restore is detected as a restore
collision (Option B) and queued for admin resolution via
'mtix sync collisions', instead of being silently renumbered. Outside a
restore window every collision renumbers normally.

This is an OPERATOR action — no client or push can advance the epoch.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncMarkRestored(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS})
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	return cmd
}

func runSyncMarkRestored(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync mark-restored: not in an mtix project (run 'mtix init' first)")
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

	epoch, err := pool.MarkRestored(ctx)
	if err != nil {
		return wrapSyncErr(stderr, "mark restored", err)
	}
	fmt.Fprintf(stdout,
		"restore window opened: hub restore-epoch is now %d.\n"+
			"Let clients reconnect and push, then run 'mtix sync collisions list' to review any restore collisions.\n",
		epoch)
	return nil
}
