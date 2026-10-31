// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestListenerGuidance_TemplatesMirrorsAndManualTeachTheListener keeps the
// agent-facing instruction set in step with the per-board listener
// (MTIX-112.20.4, Commandment 15): what starts it, how it ends, how to switch
// it off, and the doctor row with its fix.
func TestListenerGuidance_TemplatesMirrorsAndManualTeachTheListener(t *testing.T) {
	files := []string{
		"templates/skills/sync.md.tmpl", "templates/skills/multi_agent.md.tmpl",
		"templates/claude.md.tmpl", "templates/agents.md.tmpl",
		"../../.claude-plugin/skills/mtix-sync.md", "../../.codex-plugin/skills/sync/SKILL.md",
		"../../USERMANUAL.md",
	}
	for _, path := range files {
		body, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(body)
		require.Contains(t, text, "mtix daemon --listen", path)
		require.Contains(t, text, "wake.listener.no-hub-listener", path)
		require.Contains(t, text, "mtix config set sync.listen off", path)
		require.NotContains(t, text, "inert", path, "the hold is live in this build")
	}
}
