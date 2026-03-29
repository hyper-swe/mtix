// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the mtix CLI.
// mtix is an AI-native micro issue manager for code-generating LLMs.
package main

import (
	"fmt"
	"os"
)

// Version information set by ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
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
