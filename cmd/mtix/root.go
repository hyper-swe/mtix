// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// routedKey is a context key indicating the command was handled via routing.
type routedKeyType struct{}

// withRouted returns a context marked as having been routed to the server.
func withRouted(ctx context.Context) context.Context {
	return context.WithValue(ctx, routedKeyType{}, true)
}

// isRouted checks if the command was handled via server routing.
func isRouted(ctx context.Context) bool {
	v, _ := ctx.Value(routedKeyType{}).(bool)
	return v
}

// appContext holds shared state for all commands.
// Initialized in PersistentPreRunE, available to all subcommands.
type appContext struct {
	store      *sqlite.Store
	nodeSvc    *service.NodeService
	ctxSvc     *service.ContextService
	promptSvc  *service.PromptService
	agentSvc   *service.AgentService
	sessionSvc *service.SessionService
	bgSvc      *service.BackgroundService
	configSvc  *service.ConfigService
	syncSvc    *service.SyncService
	logger     *slog.Logger
	jsonOutput bool
	mtixDir    string // Path to .mtix directory, set during initApp.
}

// global app context set during PersistentPreRunE.
var app appContext

// newRootCmd creates the root Cobra command for mtix per NFR-4.2.
// Persistent flags: --json, --log-level.
// Build-time variables injected via ldflags: version, commit, date.
func newRootCmd() *cobra.Command {
	var logLevel string

	rootCmd := &cobra.Command{
		Use:   "mtix",
		Short: "AI-native micro issue manager for code-generating LLMs",
		Long: `mtix (micro-tix) is a hierarchical task management system designed
for safety-critical environments where LLM coding agents operate.

It provides micro issue tracking with infinite-depth decomposition,
prompt chain propagation, and multi-agent orchestration.`,
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return persistentPreRun(cmd, args, logLevel)
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			// Auto-export after mutation commands per FR-15.3.
			if app.syncSvc != nil && app.mtixDir != "" && isMutationCommand(cmd.Name()) {
				if err := app.syncSvc.AutoExport(cmd.Context(), app.mtixDir); err != nil {
					app.logger.Warn("auto-export failed", "error", err)
				}
			}
			return closeApp()
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().BoolVar(&app.jsonOutput, "json", false,
		"Output in JSON format")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "",
		"Override logging level (debug, info, warn, error)")

	// Register all subcommands.
	rootCmd.AddCommand(
		newInitCmd(),
		newConfigCmd(),
		newCreateCmd(),
		newMicroCmd(),
		newShowCmd(),
		newListCmd(),
		newTreeCmd(),
		newUpdateCmd(),
		newClaimCmd(),
		newUnclaimCmd(),
		newDoneCmd(),
		newDeferCmd(),
		newCancelCmd(),
		newReopenCmd(),
		newDecomposeCmd(),
		newCommentCmd(),
		newDepCmd(),
		newSearchCmd(),
		newReadyCmd(),
		newBlockedCmd(),
		newStaleCmd(),
		newOrphansCmd(),
		newStatsCmd(),
		newProgressCmd(),
		newPromptCmd(),
		newAnnotateCmd(),
		newResolveAnnotationCmd(),
		newRerunCmd(),
		newRestoreCmd(),
		newDeleteCmd(),
		newUndeleteCmd(),
		newContextCmd(),
		newAgentCmd(),
		newSessionCmd(),
		newGCCmd(),
		newVerifyCmd(),
		newBackupCmd(),
		newExportCmd(),
		newImportCmd(),
		newMigrateCmd(),
		newServeCmd(),
		newMCPCmd(),
		newDocsCmd(),
		newPluginCmd(),
		newSyncCmd(),
	)

	return rootCmd
}

// persistentPreRun handles the PersistentPreRunE logic for the root command.
// Extracted from newRootCmd to reduce cognitive complexity.
func persistentPreRun(cmd *cobra.Command, args []string, logLevel string) error {
	// Skip store init for commands that don't need it.
	if cmd.Name() == "version" || cmd.Name() == "help" {
		return nil
	}

	// Plugin install works without a .mtix/ directory.
	if cmd.Name() == "install" && cmd.Parent() != nil && cmd.Parent().Name() == "plugin" {
		// Try to init app for config access, but don't fail if .mtix/ is missing.
		_ = initApp(cmd, logLevel)
		return nil
	}

	// Check for running server and route through REST API per FR-14.1b.
	if port := shouldRouteToServer(cmd); port > 0 {
		if err := routeToServer(cmd, args, port); err != nil {
			return err
		}
		cmd.SetContext(withRouted(cmd.Context()))
		return nil
	}

	if err := initApp(cmd, logLevel); err != nil {
		return err
	}

	// Auto-import tasks.json if hash changed (FR-15.2).
	// Skip for excluded commands per FR-15.2c.
	if app.syncSvc != nil && app.mtixDir != "" && !shouldSkipAutoImport(cmd.Name()) {
		if err := app.syncSvc.AutoImport(cmd.Context(), app.mtixDir); err != nil {
			app.logger.Warn("auto-import failed", "error", err)
		}
	}

	return nil
}

// initApp initializes the application context: logger, store, services.
func initApp(_ *cobra.Command, logLevel string) error {
	// Configure logger.
	level := slog.LevelInfo
	switch logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	app.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	}))

	// Find .mtix directory.
	mtixDir, err := findMtixDir()
	if err != nil {
		// Not initialized yet — some commands (like init) work without store.
		app.logger.Debug("no .mtix directory found", "error", err)
		return nil
	}

	// Load config.
	configPath := filepath.Join(mtixDir, "config.yaml")
	app.configSvc, err = service.NewConfigService(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Ensure data directory exists (FR-15.2a: fresh clone has no .mtix/data/).
	dbDir := filepath.Join(mtixDir, "data")
	if mkErr := os.MkdirAll(dbDir, 0755); mkErr != nil {
		return fmt.Errorf("create data directory: %w", mkErr)
	}

	// Open SQLite store.
	app.store, err = sqlite.New(dbDir, app.logger)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	// Create broadcaster.
	broadcaster := service.NewHub(app.logger)
	clock := func() time.Time { return time.Now().UTC() }

	// Wire services.
	app.nodeSvc = service.NewNodeService(app.store, broadcaster, app.configSvc, app.logger, clock)
	app.ctxSvc = service.NewContextService(app.store, app.configSvc, app.logger)
	app.promptSvc = service.NewPromptService(app.store, broadcaster, app.logger, clock)
	app.agentSvc = service.NewAgentService(app.store, broadcaster, app.configSvc, app.logger, clock)
	app.sessionSvc = service.NewSessionService(app.store, app.configSvc, app.logger, clock)
	app.bgSvc = service.NewBackgroundService(app.store, app.configSvc, app.logger, clock)
	app.syncSvc = service.NewSyncService(app.store, app.logger, clock)
	app.mtixDir = mtixDir

	return nil
}

// closeApp releases resources when the command completes.
// closeOnce ensures closeApp is idempotent — safe to call from both
// PersistentPostRunE and defer in main(). Without this, a double-close
// would occur on the happy path (PersistentPostRunE + defer).
var closeOnce sync.Once

func closeApp() error {
	var closeErr error
	closeOnce.Do(func() {
		if app.store != nil {
			closeErr = app.store.Close()
		}
	})
	return closeErr
}

// resetCloseOnce resets the sync.Once for testing. Must only be called
// from test code to allow multiple closeApp() calls across test cases.
func resetCloseOnce() {
	closeOnce = sync.Once{}
}

// findMtixDir walks up from the current directory looking for .mtix/.
func findMtixDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	for {
		candidate := filepath.Join(dir, ".mtix")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf(".mtix directory not found (run 'mtix init' first)")
}

// shouldSkipAutoImport returns true for commands that must not trigger
// auto-import per FR-15.2c (avoids circular dependencies and init conflicts).
func shouldSkipAutoImport(cmdName string) bool {
	switch cmdName {
	case "init", "export", "import", "help", "version", "migrate", "sync":
		return true
	}
	return false
}

// withAutoExport wraps a command's RunE to always trigger auto-export after
// execution, regardless of whether RunE returns an error. This is critical
// because Cobra skips PersistentPostRunE when RunE errors — but the DB
// mutation may have already committed. Per FR-15.3b, export failure must
// not cause the primary command to fail.
func withAutoExport(fn func(cmd *cobra.Command, args []string) error) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		runErr := fn(cmd, args)
		if app.syncSvc != nil && app.mtixDir != "" {
			if exportErr := app.syncSvc.AutoExport(cmd.Context(), app.mtixDir); exportErr != nil {
				if app.logger != nil {
					app.logger.Warn("auto-export failed", "error", exportErr)
				}
			}
		}
		return runErr
	}
}

// isMutationCommand returns true for commands that modify data and should
// trigger auto-export per FR-15.3.
func isMutationCommand(cmdName string) bool {
	switch cmdName {
	case "create", "update", "done", "cancel", "decompose", "reopen",
		"delete", "undelete", "claim", "unclaim", "defer", "rerun",
		"restore", "import", "prompt", "annotate", "resolve-annotation",
		"comment", "dep", "micro", "gc", "register":
		return true
	}
	return false
}
