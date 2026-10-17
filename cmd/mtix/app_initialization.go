// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/spf13/cobra"
)

func initializeAppLogger(logLevel string) {
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
}

func initializeProcessAuthor() error {
	appAuthor, metaAuthor, authErr := resolveProcessAuthor(app.configSvc.AuthorID())
	if authErr != nil {
		return authErr
	}
	app.authorID = appAuthor
	if metaAuthor != "" {
		if setErr := app.store.SetDefaultAuthor(context.Background(), metaAuthor); setErr != nil {
			app.logger.Warn("could not persist author_id to the store; emits use it only via env",
				"error", setErr)
		}
	}
	return nil
}

// Diagnostics do not initialize mutating operator approval state.
func initializeProjectServices(cmd *cobra.Command, mtixDir string) {
	broadcaster := service.NewHub(app.logger)
	clock := func() time.Time { return time.Now().UTC() }

	app.nodeSvc = service.NewNodeService(app.store, broadcaster, app.configSvc, app.logger, clock)
	app.ctxSvc = service.NewContextService(app.store, app.configSvc, app.logger)
	app.promptSvc = service.NewPromptService(app.store, broadcaster, app.logger, clock)
	app.agentSvc = service.NewAgentService(app.store, broadcaster, app.configSvc, app.logger, clock)
	app.sessionSvc = service.NewSessionService(app.store, app.configSvc, app.logger, clock)
	app.bgSvc = service.NewBackgroundService(app.store, app.configSvc, app.logger, clock)
	app.syncSvc = newSyncService(clock, app.syncOptions)
	app.hooksDisp = service.NewHooksDispatcher(app.store, mtixDir, app.logger)
	if !isTopLevelDoctor(cmd) {
		initializeHookApprovals(cmd, mtixDir)
	}
	setProjectDir(mtixDir) // app.mtixDir and, with a hub, app.payloadWarnings (MTIX-95.12)
}

func isTopLevelDoctor(cmd *cobra.Command) bool {
	return cmd.Name() == "doctor" && (cmd.Parent() == nil || cmd.Parent().Parent() == nil)
}
