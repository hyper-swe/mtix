// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package docs

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWakeReclaimEvidenceGuidance_AcknowledgementNotReading pins what counts as delivery
// evidence for a reclaimed wake (MTIX-112.40.13): an acknowledgement, never a plain read.
func TestWakeReclaimEvidenceGuidance_AcknowledgementNotReading(t *testing.T) {
	for _, path := range []string{
		"USERMANUAL.md", "docs/AGENTS.md",
		"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl",
		"internal/docs/templates/skills/multi_agent.md.tmpl",
		".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			require.Contains(t, body, "`mtix inbox ack` (selective or cumulative)")
			require.Contains(t, strings.ToLower(body), "merely reading", "reading is not an acknowledgement")
			require.Contains(t, body, "one redelivery")
		})
	}
}
