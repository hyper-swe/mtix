// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Webhook origin consent (W15) must be learnable from every shipped
// instruction surface, provider-neutral and without credentials.
func TestWebhookOrigin_Instructions_DescribeOperatorConsent(t *testing.T) {
	root := filepath.Join("..", "..")
	paths := []string{
		"internal/docs/templates/skills/admin.md.tmpl",
		"internal/docs/templates/skills/multi_agent.md.tmpl",
		"internal/docs/templates/agents.md.tmpl",
		"internal/docs/templates/claude.md.tmpl",
		".codex-plugin/skills/admin/SKILL.md",
		".codex-plugin/skills/multi-agent/SKILL.md",
		".claude-plugin/skills/mtix-admin.md",
		".claude-plugin/skills/mtix-multi-agent.md",
		"USERMANUAL.md", "docs/AGENTS.md",
	}
	provider := regexp.MustCompile(`(?i)\b(supabase|neon|aurora|rds|cloud sql|alloydb)\b`)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, path))
			require.NoError(t, err)
			text := string(raw)
			require.Contains(t, text, "mtix hooks allow-origin <url>")
			require.Contains(t, text, "--allow-private")
			require.Contains(t, text, "operator terminal")
			require.Contains(t, text, "mtix hooks status")
			require.False(t, provider.MatchString(text) && path != "USERMANUAL.md", "no provider names in agent docs")
		})
	}
}

func TestWebhookOrigin_Runbook_CoversConfigureVerifyRecover(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, path := range []string{"internal/docs/templates/skills/admin.md.tmpl", ".claude-plugin/skills/mtix-admin.md", "USERMANUAL.md"} {
		raw, err := os.ReadFile(filepath.Join(root, path))
		require.NoError(t, err)
		text := string(raw)
		require.Contains(t, text, "mtix hooks allow-origin --allow-private", path)
		require.Contains(t, text, "mtix hooks log", path)
		require.Contains(t, text, "skipped-untrusted", path)
		require.Contains(t, text, "hooks.origins", path)
	}
}
