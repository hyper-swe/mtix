// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const annotationActivityGuidance = "With `mtix serve`, open a ticket's **Activity** tab. **All** includes comments and other events, newest first; **Comments** filters the loaded activity pages to plain and addressed comments. Each comment shows its author and, when present, recipient. Bodies use sanitized Markdown; identities stay literal text. Use **Load more** for older entries and refresh the ticket after comments arrive from another process. Viewing a comment does not acknowledge an inbox event: acknowledge explicitly after handling it."

func TestAnnotationActivityGuidance_ManualSkillsAndRenderedGuidance(t *testing.T) {
	for _, path := range []string{"USERMANUAL.md", "internal/docs/templates/skills/task_execution.md.tmpl", ".claude-plugin/skills/mtix-task-execution.md", ".codex-plugin/skills/task-execution/SKILL.md"} {
		t.Run(path, func(t *testing.T) {
			require.Contains(t, readAgentInstruction(t, filepath.Join("..", "..", path)), annotationActivityGuidance)
		})
	}
	data := minimalTemplateData()
	data.ProjectPrefix = "MTIX"
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, data, nil).Install("claude-code", false)
	require.NoError(t, err)
	rendered := readAgentInstruction(t, filepath.Join(dir, ".claude", "skills", "mtix-task-execution.md"))
	mirror := readAgentInstruction(t, filepath.Join("..", "..", ".claude-plugin", "skills", "mtix-task-execution.md"))
	require.Contains(t, rendered, annotationActivityGuidance)
	require.Contains(t, mirror, annotationActivityGuidance)
}
