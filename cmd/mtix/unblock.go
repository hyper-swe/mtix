// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
)

// newUnblockCmd creates the mtix unblock command (MTIX-44): manual recovery for
// a node left sticky-blocked. `blocked` is system-managed and normally clears
// itself when the last blocker resolves; this re-derives that from the node's
// CURRENT blockers and clears it if they are all resolved (a no-op otherwise).
func newUnblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock <id>",
		Short: "Re-derive a blocked node's status from its blockers; clear it if all are resolved",
		Long: `Re-derive a node's blocked status from its current blockers.

'blocked' is system-managed: mtix auto-blocks a node when a blocker is added and
auto-restores it when the last blocker resolves. Use 'unblock' to force that
re-derivation if a node is stuck 'blocked' even though 'mtix deps <id>' shows the
blockers resolved. It never overrides a genuine block — if an unresolved blocker
remains, the node stays blocked.`,
		Args: cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runUnblock(args[0])
		}),
	}
}

func runUnblock(id string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	ctx := context.Background()
	if err := app.store.RefreshBlocked(ctx, id); err != nil {
		return err
	}
	node, err := app.store.GetNode(ctx, id)
	if err != nil {
		return err
	}

	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(node)
	}
	if node.Status == model.StatusBlocked {
		out.WriteHuman("%s is still blocked — it has unresolved blockers (see 'mtix deps %s')\n", id, id)
		return nil
	}
	out.WriteHuman("✓ %s → %s\n", id, node.Status)
	return nil
}
