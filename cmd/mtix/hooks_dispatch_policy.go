// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/hooks"
)

// newHooksExecDispatchCmd creates `mtix hooks exec-dispatch [mode]`
// (MTIX-56.10): get or set this host's LOCAL exec dispatch policy. The mode is
// never synced — like trust, placement decisions bind to a machine.
func newHooksExecDispatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exec-dispatch [any|daemon|off]",
		Short: "Get or set this host's local exec dispatch policy",
		Long: `Get (no argument) or set which trigger on THIS host may run exec hooks:

  any     every trigger dispatches (default)
  daemon  only 'mtix daemon' runs dispatch passes here — CLI and server
          triggers defer entirely, so every wake goes through the one
          supervised process
  off     this host never runs exec hooks (inbox/webhook/append-file still
          deliver) — for hosts that only post work, like an agent sandbox

The setting is host-local and never synced (stored beside the trust hash).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if app.mtixDir == "" {
				return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
			}
			if len(args) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), hooks.ExecDispatchMode(app.mtixDir))
				return nil
			}
			if err := hooks.SaveExecDispatchMode(app.mtixDir, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ exec-dispatch: %s (host-local, never synced)\n", args[0])
			return nil
		},
	}
}
