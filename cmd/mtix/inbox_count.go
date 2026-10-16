// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// Only the root inbox command owns --count; false leaves listing unchanged.
func isInboxCountCommand(cmd *cobra.Command) bool {
	flag := cmd.Flags().Lookup("count")
	return autoImportCommandPath(cmd) == "inbox" && flag != nil && flag.Value.String() == "true"
}

// Validate before shared startup, and also for standalone inbox execution.
// Presence matters for excluded flags, including false/empty values.
func validateInboxCountFlags(cmd *cobra.Command) error {
	if autoImportCommandPath(cmd) != "inbox" {
		return nil
	}
	if cmd.Flags().Changed("all") && !isInboxCountCommand(cmd) {
		return fmt.Errorf("--all requires --count: %w", model.ErrInvalidInput)
	}
	if !isInboxCountCommand(cmd) {
		return nil
	}
	for _, flag := range []string{"wait", "format"} {
		if cmd.Flags().Changed(flag) {
			return fmt.Errorf("--count cannot be combined with --%s: %w", flag, model.ErrInvalidInput)
		}
	}
	agent, err := cmd.Flags().GetString("agent")
	if err != nil {
		return fmt.Errorf("count agent flag: %w", err)
	}
	return service.ValidateInboxAgent(agent)
}

func runInboxCount(cmd *cobra.Command, agent string, all bool) error {
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	pending, total, err := service.NewInboxService(app.store).InboxCount(cmd.Context(), agent)
	if err != nil {
		return fmt.Errorf("inbox count: %w", err)
	}
	if app.jsonOutput {
		err = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			Agent   string `json:"agent"`
			Pending int64  `json:"pending"`
			Total   int64  `json:"total"`
		}{agent, pending, total})
		if err != nil {
			return fmt.Errorf("write inbox count JSON: %w", err)
		}
		return nil
	}
	if all {
		pending = total
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%d\n", pending); err != nil {
		return fmt.Errorf("write inbox count: %w", err)
	}
	return nil
}
