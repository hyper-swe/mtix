// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// newHooksAllowOriginCmd creates `mtix hooks allow-origin` (FR-19, W15): the
// one-time, per-host operator consent a webhook origin needs before any
// delivery. It is privilege-raising, so it needs terminal stdin and no agent
// session marker.
func newHooksAllowOriginCmd() *cobra.Command {
	var allowPrivate bool
	cmd := &cobra.Command{
		Use:   "allow-origin <url>",
		Short: "Consent to webhook deliveries to one origin on this host",
		Long: "Save operator consent, outside the project, for webhook deliveries to the origin (scheme, host, port) of the URL. " +
			"Without consent a webhook hook is skipped and reported by hooks status. " +
			"Loopback, link-local and private destinations are refused unless the origin is listed with --allow-private. " +
			"Requires terminal stdin outside an agent session.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			env, err := oplocal.FromProcess()
			if err != nil {
				return fmt.Errorf("operator inputs: %w", err)
			}
			return runHooksAllowOrigin(oplocal.New(env), args[0], allowPrivate, hookStdinTerminal(), hooks.SessionMarked())
		},
	}
	cmd.Flags().BoolVar(&allowPrivate, "allow-private", false, "Also allow loopback, link-local and private destinations for this origin")
	return cmd
}

func runHooksAllowOrigin(state *oplocal.State, rawURL string, allowPrivate, terminal, session bool) error {
	entry, err := hooks.AllowOrigin(state, rawURL, allowPrivate, terminal, session)
	if err != nil {
		return err
	}
	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		return out.WriteJSON(entry)
	}
	out.WriteHuman("webhook origin %s consented (allow_private=%t)\n", entry.Origin, entry.AllowPrivate)
	return nil
}
