// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/testutil"
)

// A new bypass verb requires a store-access audit even if it claims to be
// read-only. This uses the live command registry, not a source-text scan.
func TestEnrolGuard_SkipListRegistry_RequiresStoreAccessAudit(t *testing.T) {
	testutil.OperatorState(t)
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	var got []string
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if skipsStoreInit(cmd) {
			got = append(got, cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	assert.ElementsMatch(t, []string{
		"mtix agent permissions recover", // operator-local state only
		"mtix attach", "mtix attach prompt-submit", "mtix attach session-end",
		"mtix attach session-start", "mtix attach wait",
		"mtix dispatcher", "mtix dispatcher install", "mtix dispatcher run",
		"mtix dispatcher start", "mtix dispatcher status", "mtix dispatcher stop",
		"mtix dispatcher uninstall", "mtix help", "mtix recover",
	}, got, "initApp bypass changed: audit all new board-store opens before updating this registry contract")
}
