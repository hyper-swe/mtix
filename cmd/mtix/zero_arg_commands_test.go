// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-146: commands that take no positional arguments reject stray ones
// instead of silently dropping them.
func TestZeroArgCommands_StrayPositional_ReturnsError(t *testing.T) {
	tests := []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{"gc", newGCCmd},
		{"export", newExportCmd},
		{"migrate", newMigrateCmd},
		{"serve", newServeCmd},
		{"docs generate", newDocsGenerateCmd},
		{"mcp", newMCPCmd},
		{"ready", newReadyCmd},
		{"stale", newStaleCmd},
		{"orphans", newOrphansCmd},
		{"recover", newRecoverCmd},
		{"list", newListCmd},
		{"stats", newStatsCmd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.cmd()
			require.NotNil(t, c.Args, "%s must declare an Args validator", tt.name)
			err := c.Args(c, []string{"stray"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown command")
			assert.NoError(t, c.Args(c, nil))
		})
	}
}

// Commands whose single positional is documented accept it and refuse a second.
func TestOptionalOneArgCommands_ExtraPositional_ReturnsError(t *testing.T) {
	for name, ctor := range map[string]func() *cobra.Command{
		"init": newInitCmd, "plugin install": newPluginInstallCmd, "blocked": newBlockedCmd,
	} {
		t.Run(name, func(t *testing.T) {
			c := ctor()
			require.NoError(t, c.Args(c, nil))
			require.NoError(t, c.Args(c, []string{"one"}))
			require.Error(t, c.Args(c, []string{"one", "two"}))
		})
	}
}
