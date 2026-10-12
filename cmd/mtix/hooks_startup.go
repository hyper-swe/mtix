// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/hooks"
)

func initializeHookApprovals(cmd *cobra.Command, dir string) {
	cfg, _ := hooks.Load(dir)
	found := false
	for _, h := range cfg.Hooks {
		if slices.Contains(h.Deliver, hooks.AdapterExec) {
			found = true
			break
		}
	}
	if !found || cmd == nil {
		return
	}
	if cmd.Name() == "status" && cmd.Parent() != nil && cmd.Parent().Name() == "hooks" {
		return
	}
	if cmd.Name() == "trust" {
		return
	}
	if app.firstRunErr != nil {
		app.logger.Warn("operator state is unusable", "error", app.firstRunErr, "fix", "mtix hooks status --json")
		app.firstRunErr = nil
		return
	}
	approvals, err := hooks.ProcessApprovals(dir)
	if err == nil {
		err = approvals.Migrate()
	}
	if err != nil {
		app.logger.Warn("hook approval startup refused", "error", err, "fix", "mtix hooks status --json")
	}
}
