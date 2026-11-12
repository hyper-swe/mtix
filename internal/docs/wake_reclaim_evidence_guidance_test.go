// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package docs

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// wakeReclaimGuidanceCopies lists every shipped copy of the wake reclaim guidance.
var wakeReclaimGuidanceCopies = []string{
	"USERMANUAL.md", "docs/AGENTS.md",
	"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl",
	"internal/docs/templates/skills/multi_agent.md.tmpl",
	".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md",
}

// TestWakeReclaimEvidenceGuidance_AcknowledgementNotReading pins what counts as delivery
// evidence for a reclaimed wake (MTIX-112.40.13): an acknowledgement, never a plain read.
func TestWakeReclaimEvidenceGuidance_AcknowledgementNotReading(t *testing.T) {
	for _, path := range wakeReclaimGuidanceCopies {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			require.Contains(t, body, "`mtix inbox ack` (selective or cumulative)")
			require.Contains(t, strings.ToLower(body), "merely reading", "reading is not an acknowledgement")
			require.Contains(t, body, "one redelivery")
		})
	}
}

// TestWakeReclaimGuidance_CappedMessageMayBeAcknowledged_FailedRedeliveryNotCounted pins two
// facts in every copy (MTIX-112.40.13.2, MTIX-112.40.13.5): a capped message may already be
// acknowledged rather than unread, and a failed redelivery is not counted as reclaimed.
func TestWakeReclaimGuidance_CappedMessageMayBeAcknowledged_FailedRedeliveryNotCounted(t *testing.T) {
	for _, path := range wakeReclaimGuidanceCopies {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			require.Contains(t, body, "may already be acknowledged")
			require.NotContains(t, strings.ToLower(body), "still unread")
			require.Contains(t, body, "is not counted in `data.reclaimed`")
			require.Contains(t, body, "(error, undeliverable, held or off)")
		})
	}
}
