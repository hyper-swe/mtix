// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the mtix CLI.
// mtix is an AI-native micro issue manager for code-generating LLMs.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// Version information set by ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Per FR-18.17 / MTIX-15.11.2: wrap the entry point so that any
	// panic with a DSN in scope (transport, sync, daemon code paths)
	// is redacted before it reaches the runtime printer. Recover
	// re-panics with the redacted value so the runtime stack trace
	// still surfaces — the user gets diagnostic visibility without
	// the password leaking to the terminal or a CI log.
	defer redact.Recover(nil)

	if err := run(); err != nil {
		// An empty `inbox --wait` timeout is an expected non-zero outcome, not a
		// failure — surface it via the exit code only, no scary "error:" line.
		// So are harden's pending changes: its report is already printed.
		if !errors.Is(err, errInboxWaitEmpty) && !errors.Is(err, errHardenPending) {
			printFinalError(os.Stderr, err)
		}
		// Structured exit codes per MTIX-26.8 (2 = harden pending,
		// 3 = disk full, 4 = corrupted, 5 = inbox-empty, 1 = generic).
		os.Exit(exitCodeForError(err)) //nolint:gocritic // intentional: errors flow here without panic; defer covers the panic path
	}
}

// printFinalError writes err as the CLI's final "error:" line. The text
// passes through the central scrubber scrubSyncText, so no configured
// DSN, password or URL-shaped DSN reaches the terminal, including from
// errors cobra builds around an argument (FR-18.17, MTIX-95.15).
func printFinalError(w io.Writer, err error) {
	fmt.Fprintf(w, "error: %s\n", scrubSyncText(err.Error()))
}

func run() error { return runArgs(nil) }

// runArgs executes the CLI with args in place of os.Args[1:] when args is
// non-nil. The in-process piped-stdin test drives the real entry point
// through it, so the stdin check it exercises is the production one.
func runArgs(args []string) error {
	rootCmd := newRootCmd()
	if args != nil {
		rootCmd.SetArgs(args)
	}
	// Ensure store cleanup runs even if Cobra skips PersistentPostRunE
	// (which happens when RunE returns an error). closeApp is idempotent.
	defer func() {
		if err := closeApp(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup error: %v\n", err)
		}
	}()
	return rootCmd.Execute()
}
