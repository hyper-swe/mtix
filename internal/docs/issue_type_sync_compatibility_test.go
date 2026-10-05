// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Shipped sync guidance must explain the permanent mixed-version classification gap.
import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIssueTypeSyncGuidance_RequiresEveryReplicaUpgrade(t *testing.T) {
	paths := []string{"../../docs/SYNC-DESIGN.md", "../../USERMANUAL.md", "templates/skill.md.tmpl", "../../docs/SKILL.md", "templates/skills/planning.md.tmpl", "../../.claude-plugin/skills/mtix-planning.md", "../../.codex-plugin/skills/planning/SKILL.md"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Contains(t, string(content), "Upgrade every replica before relying on synchronized issue type.")
			assert.Contains(t, string(content), "A node created while a 0.5.4 replica is attached stays unclassified on that replica after upgrade; already-applied create events are not replayed.")
		})
	}
}
