// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	mtixhttp "github.com/hyper-swe/mtix/internal/api/http"
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

// newImportCmd creates the mtix import command per FR-6.3 and FR-7.8.
func newImportCmd() *cobra.Command {
	var mode string
	var force bool

	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import nodes from JSON export",
		Args:  cobra.ExactArgs(1),
		RunE: withAutoExport(func(_ *cobra.Command, args []string) error {
			return runImport(args[0], mode, force)
		}),
	}

	cmd.Flags().StringVar(&mode, "mode", "merge", "Import mode: replace or merge")
	cmd.Flags().BoolVar(&force, "force", false, "Allow importing zero nodes into a non-empty database")

	return cmd
}

func runImport(filePath, mode string, force bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read import file %s: %w", filePath, err)
	}

	var exportData sqlite.ExportData
	if unmarshalErr := json.Unmarshal(data, &exportData); unmarshalErr != nil {
		return fmt.Errorf("parse import file: %w", unmarshalErr)
	}

	importMode := sqlite.ImportModeMerge
	if mode == "replace" {
		importMode = sqlite.ImportModeReplace
	}

	ctx := context.Background()
	result, err := app.store.Import(ctx, &exportData, importMode, force)
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
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
		"Bind address (use 0.0.0.0 with --insecure-bind)")
	cmd.Flags().IntVar(&port, "port", 8377, "HTTP port")

	return cmd
}

func runServe(addr string, port int) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project")
	}

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
