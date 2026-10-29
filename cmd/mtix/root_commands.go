// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// rootPostRun preserves the command post-run lifecycle (FR-15.3).
func rootPostRun(cmd *cobra.Command, _ []string) error {
	// Sync, relay and daemon commands apply foreign events, which set
	// the hub mark inside the apply transaction: bring the operator-
	// local mirror up to date (MTIX-112.17.3, ADR-007 A9).
	if shouldReconcileHubMark(cmd.CommandPath()) {
		if err := reconcileHubMarkMirror(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not mirror the hub-connected mark: %v\n", err)
		}
	}
	// MTIX-112.20.1: real activity (a mutation, an addressed send, an agent
	// heartbeat) restarts the hub-contact window. Waits and reads do not.
	if kind, ok := commandActivity(cmd); ok && app.mtixDir != "" {
		recordHubActivity(kind)
	}
	// Auto-export after mutation commands per FR-15.3, with the
	// interval-gated rolling backup riding the same trigger
	// (FR-26.6).
	if app.syncSvc != nil && app.mtixDir != "" && isMutationCommand(cmd.Name()) {
		if err := app.syncSvc.AutoExport(cmd.Context(), app.mtixDir); err != nil {
			app.logger.Warn("auto-export failed", "error", err)
		}
		maybeAutoBackup()
		// FR-19.3: fire hooks for events this command journaled. Runs
		// post-commit — never blocks or fails the mutation; adapter
		// errors are logged inside Dispatch. Events from a command that
		// errored (skipping this path) are picked up by the next one.
		app.hooksDisp.Dispatch(cmd.Context())
	}
	// Rewrite stale or marked release-defined attach artifacts (shim, glue)
	// without a terminal; grants are never rewritten (ADR-007 §6.5).
	// Top-level doctor repairs attach artifacts only through its registered --fix fixers.
	if !isTopLevelDoctor(cmd) {
		refreshAttachArtifacts(cmd.Context(), app.logger)
	}
	return finishCommand(cmd) // payload warnings (MTIX-95.12), then closeApp
}

func registerRootCommands(rootCmd *cobra.Command) {
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
		newUnblockCmd(),
		newDecomposeCmd(),
		newCommentCmd(),
		newInboxCmd(),
		newDepCmd(),
		newSearchCmd(),
		newReadyCmd(),
		newBlockedCmd(),
		newStaleCmd(),
		newOrphansCmd(),
		newProjectsCmd(),
		newStatsCmd(),
		// Hidden harness-glue verbs; the handshake serves N and N-1 and marks
		// N-1 glue for refresh (ADR-007 §6.5).
		newAttachCmd(),
	)
	registerRemainingRootCommands(rootCmd)
}

func registerRemainingRootCommands(rootCmd *cobra.Command) {
	rootCmd.AddCommand(
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
		newRecoverCmd(),
		newMigrateCmd(),
		newServeCmd(),
		newMCPCmd(),
		newDocsCmd(),
		newPluginCmd(),
		newSyncCmd(),
		newDaemonCmd(),
		newHooksCmd(),
		newDoctorCmd(),
	)
}
