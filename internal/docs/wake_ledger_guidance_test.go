// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertWakeGuidance(t *testing.T, body string) {
	t.Helper()
	for _, phrase := range []string{
		"dispatch protocol 1", "before every pass, even an empty pass",
		"`wake.version-skew`", "60-second lease", "fresh\nattempt token",
		"stale\ncompletion is refused", "attempted send error is terminal",
		"delivery is at least once", "acknowledge events explicitly after handling",
		"held registrations do not stop it", "not automatically pruned",
		"Experimental host dispatch is available but disabled by default; enrollment\nis service-only until setup integration ships",
	} {
		assert.Contains(t, body, phrase)
	}
}

func TestWakeLedgerGuidance_ManualTemplateAndSeparateMirrors(t *testing.T) {
	for _, path := range []string{
		"USERMANUAL.md", "internal/docs/templates/skills/multi_agent.md.tmpl",
		".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md",
	} {
		t.Run(path, func(t *testing.T) {
			assertWakeGuidance(t, readAgentInstruction(t, filepath.Join("..", "..", path)))
		})
	}
}

func TestWakeLedgerGuidance_InstallerRendersSkillAndBriefing(t *testing.T) {
	data := minimalTemplateData()
	data.ProjectPrefix = "MTIX"
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, data, nil).Install("claude-code", false)
	require.NoError(t, err)
	assertWakeGuidance(t, readAgentInstruction(t, filepath.Join(dir, ".claude", "skills", "mtix-multi-agent.md")))
	briefing := readAgentInstruction(t, filepath.Join(dir, "MTIX.md"))
	assert.Contains(t, briefing, "`wake.ledger` reports retained rows and protocol refusal")
	assert.Contains(t, briefing, "multi-agent skill")
}

func TestWakeLedgerGuidance_ReadOnlyDoctorAndProtocolRemedy(t *testing.T) {
	body := doctorGuidanceSection(t, filepath.Join("..", "..", "USERMANUAL.md"))
	for _, phrase := range []string{
		"`wake.ledger`", "including when all counts are zero",
		"never lower the board's protocol floor", "read-only",
		"does not prune wake rows", "reset claims", "grant permissions",
	} {
		assert.Contains(t, body, phrase)
	}
}
