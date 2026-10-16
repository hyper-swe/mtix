// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentPermissions_Docs_ShipOperatorCommandsLocalLogAndTrustedHubPolicy(t *testing.T) {
	paths := []string{
		"internal/docs/templates/skills/admin.md.tmpl",
		".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md",
		"USERMANUAL.md",
	}
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
			require.NoError(t, err)
			for _, phrase := range []string{"mtix agent allow", "mtix agent deny", "confirmations.jsonl", "mtix doctor", "share a hub only with trusted principals"} {
				assert.Contains(t, string(data), phrase)
			}
		})
	}
	manual, err := os.ReadFile(filepath.Join("..", "..", "USERMANUAL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(manual), "Trust and grants")
}
