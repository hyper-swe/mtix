// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
)

// newPluginCmd creates the `mtix plugin` command group.
func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Install mtix as a plugin for AI coding agents",
		Long: `Install skill files, reference checklists, and MCP configuration
for IDE-specific AI agent integrations.

Supported targets: claude-code (default), cursor, windsurf.`,
	}

	cmd.AddCommand(newPluginInstallCmd())

	return cmd
}

// newPluginInstallCmd creates the `mtix plugin install` subcommand.
func newPluginInstallCmd() *cobra.Command {
	var target string
	var global bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install skill files and MCP configuration",
		Long: `Install 5 skill files and 4 compliance reference checklists for the
target IDE's AI agent. Skills include safety-critical operating procedures
as baseline (not optional) — context chain traversal, independent verification,
traceability, and anomaly reporting.

For Claude Code: writes to .claude/skills/ (or ~/.claude/skills/ with --global).`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPluginInstall(target, global)
		},
	}

	cmd.Flags().StringVar(&target, "target", "claude-code",
		"Target IDE: claude-code, cursor, windsurf")
	cmd.Flags().BoolVar(&global, "global", false,
		"Install to global skill directory (~/.claude/skills/)")

	return cmd
}

// runPluginInstall executes the plugin install command.
func runPluginInstall(target string, global bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	// Save JSON output flag before newRootCmd() re-registers the persistent
	// flag (which resets app.jsonOutput to its default value of false).
	wantJSON := app.jsonOutput

	// Build template data from runtime introspection.
	rootCmd := newRootCmd()
	reg := mcp.NewToolRegistry()

	// Try to read project prefix from config.
	prefix := "PROJ"
	if app.configSvc != nil {
		if p, getErr := app.configSvc.Get("prefix"); getErr == nil && p != "" {
			prefix = p
		}
	}

	data := docs.BuildTemplateData(rootCmd, reg, prefix, version)

	installer := docs.NewPluginInstaller(cwd, data, slog.Default())

	results, err := installer.Install(target, global)
	if err != nil {
		return fmt.Errorf("plugin install: %w", err)
	}

	if wantJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	fmt.Printf("Installed %d files for %s:\n\n", len(results), target)
	for _, r := range results {
		fmt.Printf("  %-12s %s\n", r.Action+":", r.File)
	}
	fmt.Printf("\nSkills are ready. Safety-critical operating procedures are baked in.\n")

	return nil
}
