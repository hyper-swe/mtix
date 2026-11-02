// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests verify discoverable flush limits and refresh of existing managed
// instructions, rather than claiming that text establishes physical durability.
func TestDurabilityInstructions_Runbooks_DescribeLimitsAndRecovery(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md", "USERMANUAL.md", "internal/oplocal/durability.md",
	} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			for _, term := range []string{"F_FULLFSYNC", "ENOTSUP", "FlushFileBuffers", "publication", "power-loss"} {
				require.Contains(t, string(raw), term)
			}
			require.Contains(t, string(raw), "preserve")
		})
	}
}

func TestDurabilityInstructions_ManagedRefresh_PreservesCustomContent(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			original := "# Local guide\ncustom operator text\n<!-- AUTO-GENERATED: HEALTH_DIAGNOSTICS -->\nold health\n<!-- END AUTO-GENERATED -->\ncustom footer\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0600))
			gen, err := NewEmbeddedGenerator(dir, minimalTemplateData(), nil)
			require.NoError(t, err)
			_, err = gen.Generate(true)
			require.NoError(t, err)
			first, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(first), "custom operator text")
			require.Contains(t, string(first), "custom footer")
			health := extractSections(string(first))["HEALTH_DIAGNOSTICS"]
			require.Contains(t, health, "flush failure can follow publication")
			require.Contains(t, health, "mtix hooks status --json")
			require.Contains(t, health, "preserve")
			require.Contains(t, health, "power-loss")
			_, err = gen.Generate(true)
			require.NoError(t, err)
			again, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, first, again)
			if name == "AGENTS.md" {
				tracked, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
				require.NoError(t, err)
				require.Equal(t, health, extractSections(string(tracked))["HEALTH_DIAGNOSTICS"])
			}
		})
	}
}
