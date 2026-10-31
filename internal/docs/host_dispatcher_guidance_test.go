// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package docs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostDispatcherGuidance_ExperimentalEnrollmentAndCosts(t *testing.T) {
	for _, path := range []string{"USERMANUAL.md", "internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"} {
		t.Run(path, func(t *testing.T) {
			body := readAgentInstruction(t, filepath.Join("..", "..", path))
			for _, phrase := range []string{"mtix dispatcher run --experimental", "disabled by default", "no public enrollment command", "listed-only", "sync.hub_contact", "checkout's own"} {
				require.Contains(t, body, phrase)
			}
		})
	}
}
