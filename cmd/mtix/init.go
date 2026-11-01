// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newInitCmd creates the mtix init command per FR-6.3.
func newInitCmd() *cobra.Command {
	var prefix string

	cmd := &cobra.Command{
		Use:   "init [prefix]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Initialize a new mtix project",
		Long: `Initialize a new mtix project in the current directory.

Creates .mtix/ directory structure with config, database, and logs.
Creates .mtix/board-id with one stable UUID; commit it with the board.
Generates root MTIX.md, native instruction pointers and .mtix/docs/ references.
HYPERSWE.md suppresses new pointers and native instruction file creation.

The prefix may be given as an argument (mtix init MYPROJ) or with --prefix.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			chosen, err := positionalOrFlag(cmd, "prefix", prefix, args)
			if err != nil {
				return err
			}
			return runInit(chosen)
		},
	}

	cmd.Flags().StringVar(&prefix, "prefix", "PROJ",
		"Project prefix for node IDs (uppercase, max 20 chars)")

	return cmd
}

// runInit implements the mtix init command with shared FR-2.1a validation.
func runInit(prefix string) error {
	if err := model.ValidatePrefix(prefix); err != nil {
		return err
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

	if ignoreErr := addToGitignore(cwd, ".mtix/hooks.trust"); ignoreErr != nil {
		return fmt.Errorf("initialize exclusion: %w", ignoreErr)
	}

	if ignoreErr := addToGitignore(cwd, ".mtix/serve.json"); ignoreErr != nil {
		return fmt.Errorf("initialize exclusion: %w", ignoreErr)
	}

	// Create config service and initialize directory structure.
	configSvc, configErr := service.NewConfigService("")
	if configErr != nil {
		return fmt.Errorf("create config service: %w", configErr)
	}

	if initErr := configSvc.InitConfig(cwd, prefix); initErr != nil {
		return fmt.Errorf("initialize config: %w", initErr)
	}

	if _, identityErr := service.CreateBoardID(mtixDir); identityErr != nil {
		return fmt.Errorf("initialize board identity: %w", identityErr)
	}

	// Initialize SQLite database.
	dbDir := filepath.Join(mtixDir, "data")
	store, err := sqlite.New(dbDir, app.logger)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	if closeErr := store.Close(); closeErr != nil {
		return fmt.Errorf("close database: %w", closeErr)
	}

	// Generate agent documentation per FR-13.4.
	// Docs go to .mtix/docs/ — already gitignored via .mtix/ pattern.
	// Per FR-13.2: init MUST NOT add docs/ to .gitignore.
	docsDir := filepath.Join(cwd, ".mtix", "docs")
	docResults, err := generateInitDocsStrict(docsDir, prefix, version)
	if err != nil {
		return fmt.Errorf("initialize docs: %w", err)
	}

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
	results, err := generateInitDocsStrict(docsDir, prefix, ver)
	if err != nil {
		slog.Warn("doc generation failed", "error", err)
		return nil
	}
	return results
}

// generateInitDocsStrict propagates briefing migration errors to init.
func generateInitDocsStrict(docsDir, prefix, ver string) ([]docs.GenerateResult, error) {
	rootCmd := newRootCmd()
	reg := mcp.NewToolRegistry()
	data := docs.BuildTemplateData(rootCmd, reg, prefix, ver)
	data.SyncHubConfigured = hubConfigured(filepath.Dir(docsDir))
	gen, err := docs.NewEmbeddedGenerator(docsDir, data, slog.Default())
	if err != nil {
		return nil, fmt.Errorf("create doc generator: %w", err)
	}
	cleanDir := filepath.Clean(docsDir)
	// Arbitrary-output helper callers retain reference-only generation.
	if filepath.Base(cleanDir) == "docs" && filepath.Base(filepath.Dir(cleanDir)) == ".mtix" {
		gen.SetProjectRoot(filepath.Dir(filepath.Dir(cleanDir)))
	}
	results, err := gen.Generate(false)
	if err != nil {
		return nil, fmt.Errorf("generate init docs: %w", err)
	}
	return results, nil
}

// addToGitignore appends an entry to .gitignore if not already present.
// Only a regular, non-link file is read or extended; anything else is refused.
func addToGitignore(dir, entry string) (err error) {
	gitignorePath := filepath.Join(dir, ".gitignore")
	flags := os.O_RDWR | os.O_APPEND
	info, statErr := os.Lstat(gitignorePath)
	switch {
	case os.IsNotExist(statErr):
		flags |= os.O_CREATE | os.O_EXCL
		info = nil
	case statErr != nil:
		return fmt.Errorf("inspect gitignore: %w", statErr)
	case !info.Mode().IsRegular():
		return fmt.Errorf("gitignore is not a regular file; leave it unchanged and add %q by hand", entry)
	}
	f, err := os.OpenFile(gitignorePath, flags, 0o644) //nolint:gosec // operator-owned project file, type checked above and re-checked on the descriptor
	if err != nil {
		return fmt.Errorf("open gitignore: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if info != nil {
		opened, openedErr := f.Stat()
		if openedErr != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			return fmt.Errorf("gitignore changed while opening; leave it unchanged and add %q by hand", entry)
		}
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read gitignore: %w", err)
	}
	if contains(string(content), entry) {
		return nil
	}
	if len(content) > 0 && content[len(content)-1] != '\n' {
		entry = "\n" + entry
	}
	if _, err := f.WriteString(entry + "\n"); err != nil {
		return fmt.Errorf("write gitignore entry: %w", err)
	}
	return nil
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
