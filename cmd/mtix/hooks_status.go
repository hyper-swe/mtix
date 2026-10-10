// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/spf13/cobra"
)

func newHooksStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Validate operator-local state", Long: "Validate operator-local state and report its path, availability, and corrective guidance. This command does not create state or grant hook approval.", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		env, err := oplocal.FromProcess()
		if err != nil {
			return err
		}
		return runHooksStatus(oplocal.New(env))
	}}
}

func runHooksStatus(state *oplocal.State) error {
	status, err := state.Inspect()
	if err != nil {
		status.Status = "refused"
		status.Detail = err.Error()
		status.Fix = "Choose an operator-owned configuration directory outside projects, temporary directories and harness writable roots; remove symlink components and use directory mode 0700 and file mode 0600 (a user-only ACL on Windows), set XDG_CONFIG_HOME on Unix to a directory whose ancestors are owned by root or the operator and have no group or other write access (group-writable homes and unsupported network or directory-service-owned parents are refused), or choose APPDATA with supported ancestor access on Windows; then run mtix hooks status --json. Never commit or dotfile-sync this directory."
	}
	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		if outputErr := out.WriteJSON(status); outputErr != nil {
			return fmt.Errorf("write status: %w", outputErr)
		}
	} else {
		out.WriteHuman("Operator-local state: %s\nPath: %s\n", status.Status, status.Path)
		for _, limit := range status.Limitations {
			out.WriteHuman("Limit: %s\n", limit)
		}
		if status.Detail != "" {
			out.WriteHuman("%s\nFix: %s\n", status.Detail, status.Fix)
		}
		for _, report := range status.Reports {
			out.WriteHuman("%s: %s\n", report.Name, report.Reason)
		}
	}
	return err
}
