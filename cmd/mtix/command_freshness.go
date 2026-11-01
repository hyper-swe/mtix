// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// freshnessScope separates cached read output from mutation acknowledgements
// and protocol startup (FR-6.2, FR-15.2, MTIX-149). Unknown commands are unmarked.
type freshnessScope uint8

const (
	freshnessUnmarked freshnessScope = iota
	freshnessRead
	freshnessWrite
	freshnessProtocol
)

// commandFreshness follows the parsed mode of the full command path (FR-6.2).
func commandFreshness(cmd *cobra.Command, args []string) freshnessScope {
	switch autoImportCommandPath(cmd) {
	case "agent permissions recover":
		return scopeForWrite(freshnessFlag(cmd, "cancel") != "", freshnessRead)
	case "agent state":
		return scopeForWrite(freshnessFlag(cmd, "set") != "", freshnessRead)
	case "sync backfill":
		return scopeForWrite(freshnessFlag(cmd, "dry-run") != "true", freshnessRead)
	case "sync repair", "sync harden":
		return scopeForWrite(freshnessFlag(cmd, "apply") == "true", freshnessRead)
	case "sync reconcile":
		return scopeForWrite(freshnessFlag(cmd, "yes") == "true" && freshnessFlag(cmd, "dry-run") != "true", freshnessRead)
	case "sync", "doctor":
		return scopeForWrite(freshnessFlag(cmd, "fix") == "true", freshnessRead)
	case "hooks exec-dispatch":
		return scopeForWrite(len(args) > 0, freshnessRead)
	case "hooks strict":
		return scopeForWrite(len(args) > 0 && args[0] != "status", freshnessRead)
	case "hooks trust":
		return scopeForWrite(freshnessFlag(cmd, "status") != "true", freshnessRead)
	case "daemon", "sync daemon":
		return scopeForWrite(freshnessFlag(cmd, "install") == "true", freshnessProtocol)
	default:
		return fixedCommandFreshness(autoImportCommandPath(cmd))
	}
}

func scopeForWrite(write bool, otherwise freshnessScope) freshnessScope {
	if write {
		return freshnessWrite
	}
	return otherwise
}

func freshnessFlag(cmd *cobra.Command, name string) string {
	if flag := cmd.Flags().Lookup(name); flag != nil {
		return flag.Value.String()
	}
	return ""
}

// fixedCommandFreshness explicitly declares current commands. A newly added
// command gets no freshness fields until its read semantics are declared (FR-6.2).
func fixedCommandFreshness(path string) freshnessScope {
	switch path {
	case "dispatcher status", "agent list", "agent work", "blocked", "config get", "context",
		"daemon status", "dep show", "hooks fire", "hooks list", "hooks log",
		"hooks status", "inbox", "inbox status", "list", "orphans", "progress", "projects",
		"ready", "search", "session summary", "show", "stale", "stats",
		"sync collisions list", "sync conflicts list", "sync doctor",
		"sync quarantine list", "sync relay status", "sync status", "tree", "url", "verify":
		return freshnessRead
	case "attach", "attach prompt-submit", "attach session-end", "attach session-start",
		"attach wait", "serve", "mcp", "dispatcher run",
		"hook claude-session":
		return freshnessProtocol
	case "agent allow", "agent deny", "agent heartbeat", "agent register", "agent unregister", "annotate",
		"backup", "cancel", "claim", "comment", "config delete", "config set",
		"create", "dispatcher install", "dispatcher start", "dispatcher stop", "dispatcher uninstall", "daemon install", "daemon start", "daemon stop", "daemon uninstall",
		"decompose", "defer", "delete", "dep add", "dep remove", "docs generate", "hooks allow-origin",
		"done", "export", "gc", "import", "inbox ack", "inbox lease acquire",
		"inbox lease release", "init", "micro", "migrate", "plugin install",
		"prompt", "recover", "reopen", "rerun", "resolve-annotation", "restore",
		"session end", "session start", "sync backup", "sync clone",
		"sync collisions resolve", "sync conflicts resolve", "sync init", "sync mark-restored",
		"sync migrate", "sync pull", "sync push", "sync quarantine acknowledge", "sync relay attach", "sync relay clone",
		"sync relay init", "sync relay republish", "sync relay reset-peer", "sync relay retire-peer",
		"sync relay rotate-key", "sync relay tick", "sync repair-uids", "unblock", "unclaim",
		"undelete", "update":
		// Even previewing hub migration/UID repair backfills local UIDs.
		return freshnessWrite
	default:
		return freshnessUnmarked
	}
}

// wrapFreshnessCompletion reports a completed write before any post-run hook
// can fail, preserving handler errors and suppressing routed notices (FR-15.2).
func wrapFreshnessCompletion(root *cobra.Command) {
	if run := root.RunE; run != nil {
		root.RunE = func(cmd *cobra.Command, args []string) error {
			busyWrite := app.outputScope == freshnessWrite && app.staleReason == "sync_lock_busy" && !isRouted(cmd.Context())
			if err := run(cmd, args); err != nil {
				return err
			}
			if busyWrite {
				writeFreshnessNotice(cmd, "mtix: write succeeded; do not repeat this command; sync lock was busy, so auto-import could not check the pulled board; retry reads after the current owner finishes")
			}
			return nil
		}
	}
	for _, child := range root.Commands() {
		wrapFreshnessCompletion(child)
	}
}

// Notice I/O is advisory: it must not turn a committed write into an error
// that invites repetition. Log notice failure when a logger is available (FR-6.2).
func writeFreshnessNotice(cmd *cobra.Command, notice string) {
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), notice); err != nil && app.logger != nil {
		app.logger.Warn("could not write freshness notice", "error", err)
	}
}
