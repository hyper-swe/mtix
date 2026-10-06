// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Integration guidance preserves classification recovery and atomic create instructions (FR-13).
package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifiedCreateGuidance_PreservesAtomicAllocationAndRecovery(t *testing.T) {
	paths := []string{"../../USERMANUAL.md", "templates/skill.md.tmpl", "../../.claude-plugin/skills/mtix-planning.md"}
	statements := []string{
		"must match `^[A-Z][A-Z0-9-]{0,19}$`",
		"`mtix update TEST-1 --type refactor`",
		"List `--type` filters `node_type`",
		"List `--issue-type bug,feature`",
		"before writes, deduplication, held-event acknowledgement and LWW comparison",
		"A 0.5.4 replica quarantines `issue_type` updates and continues pulling other events.",
		"After upgrading, run `mtix sync pull` to retry them automatically",
		"Use `mtix sync quarantine list` to inspect any events still held.",
		"Creation, its sequence allocation and either explicit or parent auto-claim commit together.",
		"Any refused create leaves the counter unchanged; a failed claim leaves no node, creation/claim activity or sync events.",
		"The next accepted create takes the next consecutive number.",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, statement := range statements {
				assert.Contains(t, string(content), statement)
			}
			assert.NotContains(t, string(content), "<<<<<<<")
			assert.NotContains(t, string(content), ">>>>>>>")
			assert.NotContains(t, string(content), "sync quarantine retry")
		})
	}
}
