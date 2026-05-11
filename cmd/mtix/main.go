// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the mtix CLI.
// mtix is an AI-native micro issue manager for code-generating LLMs.
package main

import (
	"fmt"
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
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1) //nolint:gocritic // intentional: errors flow here without panic; defer covers the panic path
	}
}

func run() error {
	rootCmd := newRootCmd()
	// Ensure store cleanup runs even if Cobra skips PersistentPostRunE
	// (which happens when RunE returns an error). closeApp is idempotent.
	defer func() {
		if err := closeApp(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup error: %v\n", err)
		}
	}()
	return rootCmd.Execute()
}
