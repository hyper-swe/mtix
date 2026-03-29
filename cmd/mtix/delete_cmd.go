// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newDeleteCmd creates the mtix delete command per FR-6.3.
func newDeleteCmd() *cobra.Command {
	var cascade bool

	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Soft-delete a node",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runDelete(args[0], cascade)
		}),
	}

	cmd.Flags().BoolVar(&cascade, "cascade", false, "Delete all descendants too")

	return cmd
}

func runDelete(id string, cascade bool) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.nodeSvc.DeleteNode(ctx, id, cascade, "cli"); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"id": id, "status": "deleted"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Deleted %s\n", id)
	}
	return nil
}

// newUndeleteCmd creates the mtix undelete command per FR-6.3.
func newUndeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undelete <id>",
		Short: "Restore a soft-deleted node",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runUndelete(args[0])
		}),
	}
}

func runUndelete(id string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.nodeSvc.UndeleteNode(ctx, id); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"id": id, "status": "undeleted"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Undeleted %s\n", id)
	}
	return nil
}
