// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Render through the real embedded installer/generator: these assertions cover
// instructions delivered to customer projects, rather than template source alone.
func renderedHandoffSkill(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	data := minimalTemplateData()
	data.ProjectPrefix = "MTIX"
	_, err := NewPluginInstaller(dir, data, nil).Install("claude-code", false)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", name))
	require.NoError(t, err)
	return string(body)
}
func handoffGuidanceSection(t *testing.T, body, heading string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, heading+"\n")
	require.True(t, ok, "rendered instructions need their own %s heading", heading)
	section, _, _ := strings.Cut(after, "\n## ")
	return section
}
func TestPushHandoffGuidance_RenderedSectionSeparatesMirrorExport(t *testing.T) {
	body := renderedHandoffSkill(t, "mtix-sync.md")
	push := handoffGuidanceSection(t, body, "## Push lock contention")
	assert.Contains(t, push, "`mtix sync push`")
	mirror := handoffGuidanceSection(t, body, "## Pending mirror exports")
	assert.NotContains(t, mirror, "push lock", "hub push guidance must not sit in mirror export recovery")
	assert.Contains(t, mirror, "read-only WAL snapshot", "existing snapshot guidance stays in the mirror section")
}
func TestPushHandoffGuidance_RenderedNamesNonPusherOwners(t *testing.T) {
	body := renderedHandoffSkill(t, "mtix-sync.md")
	var ownership string
	for _, paragraph := range strings.Split(body, "\n\n") {
		if strings.Contains(paragraph, "non-pusher") {
			ownership = paragraph
			break
		}
	}
	require.NotEmpty(t, ownership, "rendered skill must explain non-pusher ownership")
	for _, owner := range []string{"automatic import", "backfill", "merge-import UID adoption"} {
		assert.Contains(t, ownership, owner, "contended pushes stay pending under each real non-pusher owner")
	}
}
func TestPushHandoffGuidance_RenderedAdminExplainsPerPassSummaries(t *testing.T) {
	body := renderedHandoffSkill(t, "mtix-admin.md")
	var summary string
	for _, paragraph := range strings.Split(body, "\n\n") {
		if strings.Contains(paragraph, "push complete:") {
			summary = paragraph
			break
		}
	}
	require.NotEmpty(t, summary, "actual admin push summary guidance")
	assert.Contains(t, summary, "up to three", "one invocation can print several summaries")
	assert.Contains(t, summary, "per-pass", "summary totals describe each pass")
}
func TestPushHandoffGuidance_RenderedManagedSyncIsConcise(t *testing.T) {
	dir := t.TempDir()
	data := minimalTemplateData()
	data.SyncHubConfigured = true
	gen, err := NewEmbeddedGenerator(dir, data, nil)
	require.NoError(t, err)
	_, err = gen.Generate(false)
	require.NoError(t, err)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			var guidance string
			for _, paragraph := range strings.Split(string(body), "\n\n") {
				if strings.Contains(paragraph, "`mtix sync push`") && strings.Contains(paragraph, "push lock") {
					guidance = paragraph
					break
				}
			}
			require.NotEmpty(t, guidance, "managed sync section retains a discoverable contention pointer")
			assert.LessOrEqual(t, len(strings.Fields(guidance)), 45, "managed text is concise; full runbook belongs in skill")
			assert.LessOrEqual(t, strings.Count(guidance, ". "), 0, "one concise sentence carries the pointer")
			assert.Contains(t, guidance, "sync skill", "managed guidance points to the detailed skill")
		})
	}
}
