// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
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
// recoverNextStep is the hint printed after a salvage. The replace import is
// gated by a typed ticket count whenever the target store holds tickets, so
// it cannot run unattended (MTIX-107.74).
const recoverNextStep = "Next: review the file, then run 'mtix import --mode replace' on it in a " +
	"fresh project. When the store holds tickets, it asks for the ticket count, typed at an interactive " +
	"terminal; no flag supplies it, so it cannot run unattended."

func newRecoverCmd() *cobra.Command {
	var fromExport string
	cmd := &cobra.Command{
		Use:   "recover",
		Args:  cobra.NoArgs,
		Short: "Salvage data from a damaged database into an importable export",
		Long: `Reads the project database read-only — even when it fails integrity
checks — salvaging readable data with explicit incomplete reports, fills gaps from the
.mtix/tasks.json mirror, synthesizes placeholders for lost parents, and
writes an importable export with a fresh checksum to .mtix/.

Readable addressed mail is recovered. Acknowledgement coverage is retained
only when the WAL and integrity checks finish and inbox salvage is complete.
Any unproven result discards all recovered coverage, including supplied-export
coverage, and can deliver already-read mail again. A five-second check timeout
does not prove damage: acknowledgement coverage could not be proven. If the
store opens, use mtix export to preserve readable acknowledgement coverage.
Zero-byte and one-byte databases are skipped before any SQLite open. The WAL
alone is not salvaged; it may hold the only copy of committed records. Other
commands refuse an empty, one-byte or missing database beside a nonempty WAL,
even when integrity checks are bypassed. Use mtix recover; before restoring,
copy or move the database and its -wal and -shm together out of the project
(or the entire data/ directory). Never move only the database. Mirror and
private-export fallback remains incomplete. A changed or unverifiable database
size after salvage also makes completeness unproven and discards coverage.
WAL headers and frame checksums are checked before opening the store. An
unverifiable tail, even a stale tail, makes recovery partial: committed records
(including tickets and mail) may be missing and the lost count is unknown.
A WAL that is absent, emptied, or shortened exactly on a frame boundary
(including a header-only log) cannot be distinguished from a shorter healthy log;
committed records may be missing even under a complete report.
Keep the database and WAL together.
Inbox salvage walks rowids without secondary indexes. After 10,000 failed
rowid probes it stops and reports the limit and position as incomplete; an
unreadable upper bound means the lost record count is unknown. Keep the
original database and its -wal and -shm together. The recovered file is private and is added to .gitignore
before it is written, including in existing projects.
Use --from-export private-export.json to supplement the database with an
explicit private export. Ordinary tasks.json does not carry the inbox journal.
Read the salvage report for missing or unreadable mail. Treat the recovered
file as private and never commit it.

The source database is never modified. After reviewing the salvage
report, restore with:

  mtix import --mode replace .mtix/recovered-<timestamp>.json

When the store holds tickets, the replace import asks for the ticket count,
typed at an interactive terminal; no flag supplies it, so it cannot run
unattended.

Restore into this project (after moving the entire source data/ directory aside,
including the database, -wal and -shm together) or a fresh
'mtix init' project.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := runRecoverFrom(fromExport)
			return err
		},
	}
	cmd.Flags().StringVar(&fromExport, "from-export", "", "Private explicit export to supplement database salvage (includes inbox journal)")
	return cmd
}

// runRecover performs the salvage and returns the output file path.
func runRecover() (string, error) { return runRecoverFrom("") }

func runRecoverFrom(fromExport string) (string, error) {
	mtixDir, err := findMtixDir()
	if err != nil {
		return "", fmt.Errorf("not in an mtix project: %w", err)
	}

	dbPath := filepath.Join(mtixDir, "data", "mtix.db")
	mirrorPath := filepath.Join(mtixDir, "tasks.json")
	var inboxSource *sqlite.ExportData
	if fromExport != "" {
		inboxSource, err = readRecoverExport(fromExport)
		if err != nil {
			return "", err
		}
	}

	// recover skips initApp (the store may be unopenable), so the app
	// logger may not exist yet.
	logger := app.logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}

	res, err := sqlite.Recover(context.Background(), dbPath, mirrorPath, version, sqlite.RecoverOptions{Logger: logger, InboxSources: []*sqlite.ExportData{inboxSource}})
	if err != nil {
		return "", err
	}

	raw, err := json.MarshalIndent(res.Export, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal recovered export: %w", err)
	}
	outPath, err := writeSalvageFile(mtixDir, raw)
	if err != nil {
		return "", err
	}

	printRecoverReport(res, outPath)
	writeRecoverInboxNote(context.Background(), os.Stdout, dbPath)

	return outPath, nil
}

// writeSalvageFile writes the export to .mtix/recovered-<ts>.json with
// O_EXCL plus a counter suffix on collision — a salvage attempt must
// never silently overwrite an earlier one.
func writeSalvageFile(mtixDir string, raw []byte) (string, error) {
	if err := addToGitignore(filepath.Dir(mtixDir), ".mtix/recovered-*.json"); err != nil {
		return "", fmt.Errorf("protect private recovered export from git: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("recovered-%s.json", stamp)
		if i > 0 {
			name = fmt.Sprintf("recovered-%s-%d.json", stamp, i)
		}
		outPath := filepath.Join(mtixDir, name)
		f, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create recovered export: %w", err)
		}
		if _, err := f.Write(raw); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("write recovered export: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close recovered export: %w", err)
		}
		return outPath, nil
	}
	return "", fmt.Errorf("could not find a free salvage filename in %s", mtixDir)
}

// printRecoverReport renders the salvage report for humans and agents.
func printRecoverReport(res *sqlite.RecoverResult, outPath string) {
	summary := "Salvage complete"
	if res.InboxIncomplete {
		summary = "Partial salvage (inbox journal incomplete)"
	}
	fmt.Printf("%s: %d node(s) recovered into %s\n", summary,
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
	fmt.Println("\n" + recoverNextStep)
}
