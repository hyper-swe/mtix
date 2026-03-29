// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
)

// newDocsCmd creates the `mtix docs` command group per FR-13.1.
func newDocsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docs",
		Short: "Manage agent-facing documentation",
		Long: `Generate and manage agent-facing documentation files.

Produces markdown files in the project's .mtix/docs/ directory that LLM agents
read to understand how to use mtix. Generated from runtime introspection
of CLI commands, state machine, MCP tools, and configuration.`,
	}

	cmd.AddCommand(newDocsGenerateCmd())

	return cmd
}

// newDocsGenerateCmd creates the `mtix docs generate` subcommand per FR-13.1.
func newDocsGenerateCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate agent-facing documentation",
		Long: `Generate all 11 documentation files in the project's .mtix/docs/ directory.

Auto-generated files (CLI_REFERENCE.md, STATUS_MACHINE.md, TROUBLESHOOTING.md)
are always regenerated. Template-based files are only created if missing,
unless --force is used.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDocsGenerate(force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false,
		"Force regeneration of all files including template-based ones")

	return cmd
}

// runDocsGenerate executes the docs generate command per FR-13.1.
func runDocsGenerate(force bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	docsDir := filepath.Join(cwd, ".mtix", "docs")

	// Security: reject symlinks at the docs directory to prevent arbitrary file writes.
	if info, lstatErr := os.Lstat(docsDir); lstatErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				"docs directory %s is a symlink — refusing to write (security: "+
					"symlink could redirect writes to arbitrary location)", docsDir)
		}
	}

	// Also check parent .mtix dir for symlink.
	mtixDir := filepath.Dir(docsDir)
	if info, lstatErr := os.Lstat(mtixDir); lstatErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf(
				".mtix directory %s is a symlink — refusing to write (security: "+
					"symlink could redirect writes to arbitrary location)", mtixDir)
		}
	}

	// Build template data from runtime introspection per FR-13.2.
	rootCmd := newRootCmd()
	reg := mcp.NewToolRegistry()

	data := docs.BuildTemplateData(rootCmd, reg, "PROJ", version)

	// Use embedded templates compiled into the binary.
	gen, err := docs.NewEmbeddedGenerator(docsDir, data, slog.Default())
	if err != nil {
		return fmt.Errorf("create doc generator: %w", err)
	}

	results, err := gen.Generate(force)
	if err != nil {
		return fmt.Errorf("generate docs: %w", err)
	}

	for _, r := range results {
		fmt.Printf("  %-12s %s", r.Action+":", r.File)
		if r.Message != "" {
			fmt.Printf("  (%s)", r.Message)
		}
		fmt.Println()
	}

	fmt.Printf("\nGenerated %d files in %s\n", len(results), docsDir)

	return nil
}
