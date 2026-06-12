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
for AI agent integrations.

Supported targets: claude-code (default), codex, pi.`,
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
		Long: `Install agent integration files for the target AI coding agent.

claude-code: 5 skill files + 4 compliance reference checklists into
.claude/skills/ (or ~/.claude/skills/ with --global). Skills include
safety-critical operating procedures as baseline — context chain
traversal, independent verification, traceability, anomaly reporting.

codex: AGENTS.md (Codex's native instruction file) at the project root
and an MCP server entry in .codex/config.toml (or ~/.codex/ with
--global). Existing files are never modified — if config.toml exists,
the stanza to add is printed instead.

pi: AGENTS.md at the project root (or ~/.pi/agent/ with --global); pi
loads it natively. pi has no built-in MCP — setup guidance for the
community pi-mcp-adapter extension is printed.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runPluginInstall(target, global)
		},
	}

	cmd.Flags().StringVar(&target, "target", "claude-code",
		"Target agent: claude-code, codex, pi")
	cmd.Flags().BoolVar(&global, "global", false,
		"Install to the agent's global directories instead of project-local")

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

	fmt.Printf("Installed %d items for %s:\n\n", len(results), target)
	for _, r := range results {
		fmt.Printf("  %-12s %s\n", r.Action+":", r.File)
	}
	// Manual/skipped guidance must reach the user — silently dropped
	// notes are how integrations end up half-configured.
	for _, r := range results {
		if r.Note != "" {
			fmt.Printf("\n%s (%s):\n%s\n", r.File, r.Action, r.Note)
		}
	}
	fmt.Printf("\nDone. See docs/MCP-SETUP.md for agent-specific details.\n")

	return nil
}
