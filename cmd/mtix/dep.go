// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
)

// newDepCmd creates the mtix dep command group per FR-4.1.
func newDepCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dep",
		Short: "Manage node dependencies",
	}

	cmd.AddCommand(newDepAddCmd(), newDepRemoveCmd(), newDepShowCmd())
	return cmd
}

// newDepAddCmd creates the mtix dep add command.
func newDepAddCmd() *cobra.Command {
	var depType string

	cmd := &cobra.Command{
		Use:   "add <from-id> <to-id>",
		Short: "Add a dependency between two nodes",
		Args:  cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runDepAdd(args[0], args[1], depType)
		}),
	}

	cmd.Flags().StringVar(&depType, "type", "blocks",
		"Dependency type (blocks, relates_to)")

	return cmd
}

func runDepAdd(fromID, toID, depType string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	dep := &model.Dependency{
		FromID:  fromID,
		ToID:    toID,
		DepType: model.DepType(depType),
	}

	ctx := context.Background()
	if err := app.store.AddDependency(ctx, dep); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"from": fromID, "to": toID, "type": depType, "status": "added",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Added %s dependency: %s → %s\n", depType, fromID, toID)
	}
	return nil
}

// newDepRemoveCmd creates the mtix dep remove command.
func newDepRemoveCmd() *cobra.Command {
	var depType string

	cmd := &cobra.Command{
		Use:   "remove <from-id> <to-id>",
		Short: "Remove a dependency between two nodes",
		Args:  cobra.ExactArgs(2),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runDepRemove(args[0], args[1], depType)
		}),
	}

	cmd.Flags().StringVar(&depType, "type", "blocks",
		"Dependency type (blocks, relates_to)")

	return cmd
}

func runDepRemove(fromID, toID, depType string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.store.RemoveDependency(ctx, fromID, toID, model.DepType(depType)); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"from": fromID, "to": toID, "status": "removed",
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Removed %s dependency: %s → %s\n", depType, fromID, toID)
	}
	return nil
}

// newDepShowCmd creates the mtix dep show command.
func newDepShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show blockers for a node",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runDepShow(args[0])
		},
	}
}

func runDepShow(id string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	blockers, err := app.store.GetBlockers(ctx, id)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.MarshalIndent(blockers, "", "  ")
		fmt.Println(string(data))
	} else {
		if len(blockers) == 0 {
			fmt.Printf("No blockers for %s\n", id)
		} else {
			fmt.Printf("Blockers for %s:\n", id)
			for _, b := range blockers {
				fmt.Printf("  %s (%s)\n", b.FromID, b.DepType)
			}
		}
	}
	return nil
}
