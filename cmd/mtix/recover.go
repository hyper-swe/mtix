// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newRecoverCmd creates the mtix recover command (MTIX-26.5).
// It must work when the database is too damaged for the store to open,
// so persistentPreRun skips store initialization for it.
func newRecoverCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "recover",
		Short: "Salvage data from a damaged database into an importable export",
		Long: `Reads the project database read-only — even when it fails integrity
checks — salvaging every readable row individually, fills gaps from the
.mtix/tasks.json mirror, synthesizes placeholders for lost parents, and
writes an importable export with a fresh checksum to .mtix/.

The damaged database is never modified. After reviewing the salvage
report, restore with:

  mtix import --mode replace .mtix/recovered-<timestamp>.json

into this project (after moving the damaged database aside) or a fresh
'mtix init' project.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := runRecover()
			return err
		},
	}
}

// runRecover performs the salvage and returns the output file path.
func runRecover() (string, error) {
	mtixDir, err := findMtixDir()
	if err != nil {
		return "", fmt.Errorf("not in an mtix project: %w", err)
	}

	dbPath := filepath.Join(mtixDir, "data", "mtix.db")
	mirrorPath := filepath.Join(mtixDir, "tasks.json")

	// recover skips initApp (the store may be unopenable), so the app
	// logger may not exist yet.
	logger := app.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	res, err := sqlite.Recover(context.Background(), dbPath, mirrorPath, version, logger)
	if err != nil {
		return "", err
	}

	raw, err := json.MarshalIndent(res.Export, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal recovered export: %w", err)
	}
	outPath := filepath.Join(mtixDir,
		fmt.Sprintf("recovered-%s.json", time.Now().UTC().Format("20060102-150405")))
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		return "", fmt.Errorf("write recovered export: %w", err)
	}

	printRecoverReport(res, outPath)
	return outPath, nil
}

// printRecoverReport renders the salvage report for humans and agents.
func printRecoverReport(res *sqlite.RecoverResult, outPath string) {
	fmt.Printf("Salvage complete: %d node(s) recovered into %s\n",
		res.Export.NodeCount, outPath)
	fmt.Printf("  from database : %d\n", len(res.RecoveredIDs))
	fmt.Printf("  from mirror   : %d\n", len(res.FromMirror))
	if len(res.Placeholders) > 0 {
		fmt.Printf("  placeholders  : %d (lost parents synthesized: %v)\n",
			len(res.Placeholders), res.Placeholders)
	}
	if len(res.LostIDs) > 0 {
		fmt.Printf("  LOST          : %d — %v\n", len(res.LostIDs), res.LostIDs)
	}
	for _, note := range res.Notes {
		fmt.Printf("  note: %s\n", note)
	}
	fmt.Println("\nNext: review the file, then 'mtix import --mode replace' it into a fresh project.")
}
