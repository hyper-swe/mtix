// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// newConfigCmd creates the mtix config command group per FR-11.1a.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage mtix configuration",
		Long:  `Get, set, or delete configuration values using dot-notation keys.`,
	}

	cmd.AddCommand(newConfigGetCmd(), newConfigSetCmd(), newConfigDeleteCmd())
	return cmd
}

// newConfigGetCmd creates the mtix config get command.
func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a configuration value",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runConfigGet(args[0])
		},
	}
}

// newConfigSetCmd creates the mtix config set command.
func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runConfigSet(args[0], args[1])
		},
	}
}

// newConfigDeleteCmd creates the mtix config delete command.
func newConfigDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <key>",
		Short: "Delete a configuration value (revert to default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runConfigDelete(args[0])
		},
	}
}

func runConfigGet(key string) error {
	if app.configSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	v, err := app.configSvc.Get(key)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"key": key, "value": v})
		fmt.Println(string(data))
	} else {
		fmt.Println(v)
	}
	return nil
}

func runConfigSet(key, value string) error {
	if app.configSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	warning, err := app.configSvc.Set(key, value)
	if err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{
			"key": key, "value": value, "warning": warning,
		})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Set %s = %s\n", key, value)
		if warning != "" {
			fmt.Printf("Warning: %s\n", warning)
		}
	}
	return nil
}

func runConfigDelete(key string) error {
	if app.configSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	if err := app.configSvc.Delete(key); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"key": key, "status": "deleted"})
		fmt.Println(string(data))
	} else {
		fmt.Printf("Deleted %s (reverted to default)\n", key)
	}
	return nil
}
