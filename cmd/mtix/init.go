// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// prefixRegex validates project prefixes per FR-2.1a.
var prefixRegex = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,19}$`)

// newInitCmd creates the mtix init command per FR-6.3.
func newInitCmd() *cobra.Command {
	var prefix string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new mtix project",
		Long: `Initialize a new mtix project in the current directory.

Creates .mtix/ directory structure with config, database, and logs.
Generates agent documentation in .mtix/docs/.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runInit(prefix)
		},
	}

	cmd.Flags().StringVar(&prefix, "prefix", "PROJ",
		"Project prefix for node IDs (uppercase, max 20 chars)")

	return cmd
}

// runInit implements the mtix init command.
func runInit(prefix string) error {
	if !prefixRegex.MatchString(prefix) {
		return fmt.Errorf(
			"invalid prefix %q: must match ^[A-Z][A-Z0-9-]{0,19}$", prefix,
		)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	mtixDir := filepath.Join(cwd, ".mtix")

	// Check if already initialized.
	if _, statErr := os.Stat(mtixDir); statErr == nil {
		return fmt.Errorf("project already initialized at %s", mtixDir)
	}

	// Create config service and initialize directory structure.
	configSvc, configErr := service.NewConfigService("")
	if configErr != nil {
		return fmt.Errorf("create config service: %w", configErr)
	}

	if initErr := configSvc.InitConfig(cwd, prefix); initErr != nil {
		return fmt.Errorf("initialize config: %w", initErr)
	}

	// Initialize SQLite database.
	dbDir := filepath.Join(mtixDir, "data")
	store, err := sqlite.New(dbDir, app.logger)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}

	// Generate agent documentation per FR-13.4.
	// Docs go to .mtix/docs/ — already gitignored via .mtix/ pattern.
	// Per FR-13.2: init MUST NOT add docs/ to .gitignore.
	docsDir := filepath.Join(cwd, ".mtix", "docs")
	docResults := generateInitDocs(docsDir, prefix, version)

	fmt.Printf("Initialized mtix project with prefix %s\n", prefix)
	fmt.Printf("  Config:   %s\n", filepath.Join(mtixDir, "config.yaml"))
	fmt.Printf("  Database: %s\n", filepath.Join(dbDir, "mtix.db"))
	fmt.Printf("  Logs:     %s\n", filepath.Join(mtixDir, "logs"))
	fmt.Printf("  Docs:     %s\n", docsDir)

	for _, r := range docResults {
		fmt.Printf("    %s: %s\n", r.Action, r.File)
	}

	return nil
}

// generateInitDocs creates agent documentation during init per FR-13.4.
// Uses embedded templates compiled into the binary.
func generateInitDocs(docsDir, prefix, ver string) []docs.GenerateResult {
	rootCmd := newRootCmd()
	reg := mcp.NewToolRegistry()

	data := docs.BuildTemplateData(rootCmd, reg, prefix, ver)

	gen, err := docs.NewEmbeddedGenerator(docsDir, data, slog.Default())
	if err != nil {
		slog.Warn("doc generation skipped", "error", err)
		return nil
	}

	results, err := gen.Generate(false)
	if err != nil {
		slog.Warn("doc generation failed", "error", err)
		return nil
	}

	return results
}

// addToGitignore appends an entry to .gitignore if not already present.
func addToGitignore(dir, entry string) {
	gitignorePath := filepath.Join(dir, ".gitignore")

	// Read existing content.
	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return
	}

	// Check if entry already present.
	lines := string(content)
	if contains(lines, entry) {
		return
	}

	// Append entry.
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	if len(content) > 0 && content[len(content)-1] != '\n' {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(entry + "\n")
}

// contains checks if a string contains a line matching the given entry.
func contains(s, entry string) bool {
	for _, line := range splitLines(s) {
		if line == entry {
			return true
		}
	}
	return false
}

// splitLines splits a string into lines.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
