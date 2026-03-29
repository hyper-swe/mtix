// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/mcp"
)

// newMCPCmd creates the mtix mcp command per FR-14.1a.
// Runs the MCP server over stdio (stdin/stdout) for LLM agent integration.
// Logs are written to .mtix/logs/mtix.log — never to stdout — to avoid
// corrupting the JSON-RPC protocol stream.
func newMCPCmd() *cobra.Command {
	var projectDir string

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run as MCP server over stdio (for LLM agent integration)",
		Long: `Starts mtix as a Model Context Protocol (MCP) server using
stdio transport. JSON-RPC 2.0 messages are read from stdin and
responses are written to stdout.

This command is designed to be invoked by MCP clients such as
Claude Desktop, Cursor, Claude Code, or any MCP-compatible agent.

Use --project to specify the project directory. This allows a single
mtix binary to serve multiple projects without relying on cwd:

  mtix mcp --project /path/to/project-a
  mtix mcp --project /path/to/project-b

All logs are redirected to .mtix/logs/mtix.log to prevent
contamination of the protocol stream.

Example MCP client configuration:

  {
    "mcpServers": {
      "mtix": {
        "command": "mtix",
        "args": ["mcp", "--project", "/path/to/your/project"]
      }
    }
  }`,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if projectDir != "" {
				return os.Chdir(projectDir)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return runMCP()
		},
	}

	cmd.Flags().StringVarP(&projectDir, "project", "C", "",
		"Project directory containing .mtix/ (overrides cwd)")

	return cmd
}

// runMCP starts the MCP stdio server with all tools registered.
// Per MTIX-6.1.1: logs go to file, protocol goes to stdout.
func runMCP() error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}

	// Redirect logs to file — stdout is reserved for MCP protocol.
	logger, err := createFileLogger()
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}

	srv := mcp.NewServer(os.Stdin, os.Stdout, logger, version)
	reg := srv.Registry()

	// Register all MCP tool categories.
	mcp.RegisterNodeTools(reg, app.nodeSvc, app.store)
	mcp.RegisterWorkflowTools(reg, app.nodeSvc, app.store, app.bgSvc)
	mcp.RegisterContextTools(reg, app.ctxSvc, app.promptSvc)
	mcp.RegisterDepTools(reg, app.store)
	mcp.RegisterSessionTools(reg, app.sessionSvc, app.agentSvc)
	mcp.RegisterAnalyticsTools(reg, app.store, app.agentSvc, app.configSvc)
	mcp.RegisterDocsTools(reg)

	logger.Info("mtix MCP server starting",
		"version", version,
		"tools", len(reg.List()))

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return srv.Serve(ctx)
}

// createFileLogger creates a logger that writes to .mtix/logs/mtix.log.
// This keeps stdout clean for the MCP JSON-RPC protocol stream.
func createFileLogger() (*slog.Logger, error) {
	mtixDir, err := findMtixDir()
	if err != nil {
		return slog.Default(), nil
	}

	logDir := filepath.Join(mtixDir, "logs")
	if mkErr := os.MkdirAll(logDir, 0o750); mkErr != nil {
		return nil, fmt.Errorf("create log directory: %w", mkErr)
	}

	logPath := filepath.Join(logDir, "mtix.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", logPath, err)
	}

	return slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})), nil
}
