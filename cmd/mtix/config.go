// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"os"
	"strings"

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
	var agent string
	cmd := &cobra.Command{Use: "get <key>", Short: "Get a configuration value", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error { return runConfigGetForAgent(args[0], agent) }}
	cmd.Flags().StringVar(&agent, "agent", "", "Select an agent override for host-local wake settings")
	return cmd
}

// newConfigSetCmd creates the mtix config set command.
func newConfigSetCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{Use: "set <key> <value>", Short: "Set a configuration value", Args: cobra.ExactArgs(2), RunE: func(_ *cobra.Command, args []string) error { return runConfigSetForAgent(args[0], args[1], agent) }}
	cmd.Flags().StringVar(&agent, "agent", "", "Select an agent override for host-local wake settings")
	return cmd
}

// newConfigDeleteCmd creates the mtix config delete command.
func newConfigDeleteCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{Use: "delete <key>", Short: "Delete a configuration value (revert to default)", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error { return runConfigDeleteForAgent(args[0], agent) }}
	cmd.Flags().StringVar(&agent, "agent", "", "Select an agent override for host-local wake settings")
	return cmd
}

func runConfigGet(key string) error { return runConfigGetForAgent(key, "") }

func runConfigGetForAgent(key, agent string) error {
	if app.configSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	cs, err := scopedConfig(key, agent)
	if err != nil {
		return err
	}
	v, err := cs.Get(key)
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

func runConfigSet(key, value string) error { return runConfigSetForAgent(key, value, "") }

func runConfigSetForAgent(key, value, agent string) error {
	if app.configSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	cs, err := scopedConfig(key, agent)
	if err != nil {
		return err
	}
	warning, err := cs.Set(key, value)
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

func runConfigDelete(key string) error { return runConfigDeleteForAgent(key, "") }

func runConfigDeleteForAgent(key, agent string) error {
	if app.configSvc == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	cs, err := scopedConfig(key, agent)
	if err != nil {
		return err
	}
	if err := cs.Delete(key); err != nil {
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

func scopedConfig(key, agent string) (*service.ConfigService, error) {
	if agent != "" && !model.IsWakeSetting(key) {
		return nil, fmt.Errorf("--agent applies only to host wake settings: %w", model.ErrInvalidInput)
	}
	cs := app.configSvc
	if model.IsWakeSetting(key) {
		env, err := oplocal.FromProcess()
		if err != nil {
			return nil, err
		}
		cs = cs.WithWakeSettings(service.WakeConfigOptions{State: oplocal.New(env), Agent: agent})
	}
	if keys := cs.IgnoredWorkspaceKeys(); len(keys) > 0 {
		if _, err := fmt.Fprintf(os.Stderr, "Ignored workspace authority keys %q; remove them from .mtix/config.yaml and use mtix config set for host settings.\n", strings.Join(keys, ", ")); err != nil {
			return nil, fmt.Errorf("report ignored configuration: %w", err)
		}
	}
	return cs, nil
}
