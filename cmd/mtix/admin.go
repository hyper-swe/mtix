// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	mtixhttp "github.com/hyper-swe/mtix/internal/api/http"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newGCCmd creates the mtix gc command per FR-7.3.
func newGCCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gc",
		Short: "Run garbage collection (clean expired soft-deletes)",
		RunE: withAutoExport(func(_ *cobra.Command, _ []string) error {
			return runGC()
		}),
	}
}

func runGC() error {
	if app.bgSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	if err := app.bgSvc.RunScan(ctx); err != nil {
		return err
	}

	if app.jsonOutput {
		data, _ := json.Marshal(map[string]string{"status": "gc_complete"})
		fmt.Println(string(data))
	} else {
		fmt.Println("Garbage collection complete")
	}
	return nil
}

// newVerifyCmd creates the mtix verify command per FR-3.7.
func newVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify [id]",
		Short: "Verify content hash integrity",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return runVerify(id)
		},
	}
}

func runVerify(id string) error {
	if app.nodeSvc == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()

	if id != "" {
		// Verify a single node.
		node, err := app.nodeSvc.GetNode(ctx, id)
		if err != nil {
			return err
		}

		if app.jsonOutput {
			data, _ := json.Marshal(map[string]any{
				"id": id, "content_hash": node.ContentHash, "verified": true,
			})
			fmt.Println(string(data))
		} else {
			fmt.Printf("%s: hash=%s (verified)\n", id, node.ContentHash)
		}
	} else {
		// Full project verification: check all nodes' content hashes.
		nodes, _, err := app.store.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 10000})
		if err != nil {
			return fmt.Errorf("list nodes for verification: %w", err)
		}

		var mismatches []string
		for _, n := range nodes {
			expected := n.ComputeHash()
			if expected != n.ContentHash {
				mismatches = append(mismatches, n.ID)
			}
		}

		if app.jsonOutput {
			data, _ := json.Marshal(map[string]any{
				"total_nodes": len(nodes),
				"verified":    len(mismatches) == 0,
				"mismatches":  mismatches,
			})
			fmt.Println(string(data))
		} else {
			fmt.Printf("Verified %d nodes\n", len(nodes))
			if len(mismatches) > 0 {
				fmt.Printf("INTEGRITY FAILURE: %d nodes with hash mismatches:\n", len(mismatches))
				for _, id := range mismatches {
					fmt.Printf("  - %s\n", id)
				}
			} else {
				fmt.Println("All content hashes verified OK")
			}
		}
	}
	return nil
}

// newBackupCmd creates the mtix backup command per FR-7.6.
func newBackupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backup <path>",
		Short: "Create a backup of the mtix database",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runBackup(args[0])
		},
	}
}

func runBackup(path string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	ctx := context.Background()
	result, err := app.store.Backup(ctx, path)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	if app.jsonOutput {
		data, _ := json.Marshal(result)
		fmt.Println(string(data))
	} else {
		fmt.Printf("Backup created: %s (%d bytes, verified: %t)\n",
			result.Path, result.Size, result.Verified)
	}
	return nil
}

// newExportCmd creates the mtix export command per FR-6.3.
func newExportCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export nodes to JSON",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runExport(format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "Export format (json)")

	return cmd
}

func runExport(format string) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	_ = format // Only JSON supported currently.
	ctx := context.Background()

	// Use store.Export() to produce the FR-7.8 ExportData envelope with
	// version, schema_version, checksum, and node_count — matching the
	// format used by AutoExport for consistency.
	exportData, err := app.store.Export(ctx, "", "")
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}

	data, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal export: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// importFlags collects the import command's options (FR-6.3, FR-7.8;
// ADR-003 §6 reconciliation flags).
type importFlags struct {
	mode              string
	force             bool
	recomputeChecksum bool
	forceRename       bool
	confirm           bool
	remapFile         string
}

// newImportCmd creates the mtix import command per FR-6.3 and FR-7.8.
// Imports run through offline/export-import reconciliation (ADR-003 §6):
// incoming provisional nodes are renumbered to clean local numbers and every
// incoming uid is validated at the import boundary (audit F-3).
func newImportCmd() *cobra.Command {
	var f importFlags

	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import nodes from JSON export",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runImport(args[0], f)
		}),
	}

	cmd.Flags().StringVar(&f.mode, "mode", "merge",
		"Import mode: merge, or replace (DELETES every existing ticket first; typed confirmation required)")
	cmd.Flags().BoolVar(&f.force, "force", false, "Allow importing zero nodes into a non-empty database")
	cmd.Flags().BoolVar(&f.recomputeChecksum, "recompute-checksum", false,
		"Recovery only (MTIX-26.5): replace the file's checksum with one computed over its current content, accepting hand-reconstructed exports")
	cmd.Flags().BoolVar(&f.confirm, "confirm", false,
		"Confirm renumbering in a non-empty live store (ADR-003 \u00a76): incoming provisional nodes, and local tasks "+
			"whose id the file gives to a different task; without it such an import is reported but not applied")
	cmd.Flags().BoolVar(&f.forceRename, "force-rename", false,
		"On an incoming uid that collides with a different local node, re-stamp the import node with a fresh local uid instead of rejecting (ADR-003 §6)")
	cmd.Flags().StringVar(&f.remapFile, "remap-file", "",
		"Write the uid-keyed remap (uid -> new display_path) to this JSON file (ADR-003 §6)")

	return cmd
}

func runImport(filePath string, f importFlags) error {
	if app.store == nil {
		return nothingWritten(fmt.Errorf("not in an mtix project"))
	}

	exportData, err := readImportFile(filePath, f)
	if err != nil {
		return nothingWritten(err)
	}

	ctx := context.Background()
	importMode := sqlite.ImportModeMerge
	if f.mode == "replace" {
		importMode = sqlite.ImportModeReplace
		if gErr := guardImportReplace(ctx, exportData); gErr != nil {
			return gErr
		}
	}

	report, result, err := app.store.ImportReconcile(ctx, exportData, importOptions(ctx, importMode, f))

	// The report is loud whether or not the import applied (ADR-003 §6): always
	// surface it, and persist the uid-keyed remap when one was produced.
	if report != nil {
		fmt.Fprint(os.Stderr, report.String())
		if writeErr := writeRemapFile(f.remapFile, report); writeErr != nil {
			if !report.Applied {
				return nothingWritten(writeErr)
			}
			return writeErr
		}
	}
	if err != nil {
		// Every import error but ErrImportIncomplete leaves the store
		// unchanged (MTIX-95.31.1), so the auto-export is skipped for it.
		if errors.Is(err, sqlite.ErrImportIncomplete) {
			return fmt.Errorf("import failed: %w", err)
		}
		return nothingWritten(fmt.Errorf("import failed: %w", err))
	}

	// An import of the tasks.json whose auto-import was refused resolves
	// that refusal, so the auto-export after it rewrites the board
	// (MTIX-95.31.2).
	if app.syncSvc != nil && app.mtixDir != "" {
		if resolveErr := app.syncSvc.ResolveRefusalByImport(app.mtixDir, filePath); resolveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", resolveErr)
		}
	}

	if app.jsonOutput {
		out, _ := json.Marshal(result)
		fmt.Println(string(out))
	} else {
		fmt.Printf("Import complete: %d created, %d updated, %d skipped, %d deps\n",
			result.NodesCreated, result.NodesUpdated,
			result.NodesSkipped, result.DepsImported)
	}
	return nil
}

// importOptions returns the reconciliation options of an import. A merge
// takes the verified pre-import backup the automatic import takes, once
// every check has passed and just before it writes, and says where it is
// (MTIX-95.31.4).
func importOptions(ctx context.Context, mode sqlite.ImportMode, f importFlags) sqlite.ImportReconcileOptions {
	opts := sqlite.ImportReconcileOptions{
		Mode:        mode,
		Force:       f.force,
		ForceRename: f.forceRename,
		Confirm:     f.confirm,
	}
	if mode == sqlite.ImportModeMerge && app.syncSvc != nil && app.mtixDir != "" {
		opts.BeforeWrite = func() error {
			backup, err := app.syncSvc.BackupBeforeImport(ctx, app.mtixDir)
			if err != nil {
				return err
			}
			if rel, relErr := filepath.Rel(filepath.Dir(app.mtixDir), backup); relErr == nil {
				backup = rel
			}
			fmt.Fprintf(os.Stderr, "mtix: local database backed up to %s before the merge\n", backup)
			return nil
		}
	}
	return opts
}

// writeRemapFile persists the uid-keyed remap (uid -> new display_path) produced
// by reconciliation to path, when path is non-empty and a remap exists
// (ADR-003 §6 — the remap file external references reconcile against). It
// holds the renumbered provisional nodes, the local tasks a merge renumbers
// because the file holds a different task under their id, and the local
// tasks it moves to the id the file holds them under (MTIX-95.31.4); a node
// without a uid has no key and is left out.
func writeRemapFile(path string, report *sqlite.ImportReconcileReport) error {
	moved := append(append(append([]sqlite.ImportRemapEntry{}, report.Remaps...), report.LocalRenumbers...),
		report.Moved...)
	if path == "" || len(moved) == 0 {
		return nil
	}
	remap := make(map[string]string, len(moved))
	for _, m := range moved {
		if m.UID != "" {
			remap[m.UID] = m.NewPath
		}
	}
	out, err := json.MarshalIndent(remap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode remap file: %w", err)
	}
	if writeErr := os.WriteFile(path, out, 0o600); writeErr != nil {
		return fmt.Errorf("write remap file %s: %w", path, writeErr)
	}
	return nil
}

// newMigrateCmd creates the mtix migrate command.
func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("Database is auto-migrated on startup")
			return nil
		},
	}
}

// newServeCmd creates the mtix serve command per FR-6.4.
func newServeCmd() *cobra.Command {
	var (
		addr string
		port int
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the mtix HTTP/WebSocket/gRPC server",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runServe(addr, port)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1",
		"Bind address (default localhost; non-localhost exposes API without auth)")
	cmd.Flags().IntVar(&port, "port", 8377, "HTTP port")

	return cmd
}

func runServe(addr string, port int) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	// Mirror parity per FR-15.3 / MTIX-26.1: serve mutations reach the
	// tasks.json mirror without a process exit.
	defer wireMirrorExporter(app.logger)()

	// MTIX-53: dispatch hooks on every mutation host-side, same as the CLI and
	// MCP server, so a REST/gRPC mutation can fire an exec cold-start. After the
	// mirror wiring so it keeps on-commit slot 0.
	defer wireHookDispatch()()

	clock := func() time.Time { return time.Now().UTC() }

	srv := mtixhttp.NewServer(
		app.store,
		app.nodeSvc,
		app.bgSvc,
		app.sessionSvc,
		app.agentSvc,
		app.configSvc,
		app.logger,
		mtixhttp.ServerConfig{
			Bind: addr,
			Port: fmt.Sprintf("%d", port),
		},
		clock,
	)

	fmt.Printf("Starting mtix server at %s:%d\n", addr, port)
	return srv.ListenAndServeWithGracefulShutdown()
}

// importReplaceOp describes what 'mtix import --mode replace' destroys:
// every existing node, replaced by the file's nodes.
func importReplaceOp(ctx context.Context, incoming int) (destructiveOp, error) {
	nodes, err := countRows(ctx, "nodes")
	if err != nil {
		return destructiveOp{}, err
	}
	return destructiveOp{
		Command: "mtix import --mode replace",
		Scope:   localScope(ctx),
		Destroys: []destroyCount{
			{Label: "existing tickets (every node, every project, including soft-deleted)", N: nodes},
		},
		Consequence: fmt.Sprintf("the store holds exactly the %d node(s) in the import file; "+
			"any ticket not in that file is gone", incoming),
		SnapshotTag: "import-replace",
	}, nil
}

// guardImportReplace runs the MTIX-90 gate for replace mode, which runs
// DELETE FROM nodes before it inserts the file's content, so every ticket
// not in the file is lost. A file the import will reject must not ask
// anyone to confirm a delete first, so the checksum is verified before
// the prompt (the store verifies it again on import).
func guardImportReplace(ctx context.Context, exportData *sqlite.ExportData) error {
	valid, err := sqlite.VerifyExportChecksum(exportData)
	if err != nil {
		return fmt.Errorf("import failed: verify checksum: %w", err)
	}
	if !valid {
		return fmt.Errorf("import failed: checksum verification failed: %w", model.ErrInvalidInput)
	}
	op, err := importReplaceOp(ctx, len(exportData.Nodes))
	if err != nil {
		return err
	}
	_, err = guardDestructive(ctx, os.Stderr, op)
	return err
}
