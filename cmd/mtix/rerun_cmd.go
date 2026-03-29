// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// newRerunCmd creates the mtix rerun command per FR-6.3.
func newRerunCmd() *cobra.Command {
	var (
		strategy string
		reason   string
	)

	cmd := &cobra.Command{
		Use:   "rerun <id>",
		Short: "Invalidate and reprocess descendants of a node",
		Long: `Rerun invalidates descendants with configurable strategies:
  --strategy=all        Reset all descendants to open
  --strategy=open_only  Reset only non-done descendants
  --strategy=delete     Soft-delete all descendants
  --strategy=review     Set descendants to invalidated for review`,
		Args: cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runRerun(args[0], strategy, reason)
		}),
	}

	cmd.Flags().StringVar(&strategy, "strategy", "all",
		"Rerun strategy (all, open_only, delete, review)")
	cmd.Flags().StringVar(&reason, "reason", "rerun via CLI",
		"Reason for the rerun")

	return cmd
}

func runRerun(id, strategy, reason string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.nodeSvc.Rerun(
		ctx, id, service.RerunStrategy(strategy), reason, "cli",
	); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"id": id, "strategy": strategy, "status": "rerun_initiated",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Rerun %s with strategy %s\n", id, strategy)
	}
	return nil
}

// newRestoreCmd creates the mtix restore command per FR-3.5.
func newRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id>",
		Short: "Restore an invalidated node to its previous status",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runRestore(args[0])
		}),
	}
}

func runRestore(id string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.nodeSvc.Restore(ctx, id, "cli"); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"id": id, "status": "restored"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Restored %s\n", id)
	}
	return nil
}
