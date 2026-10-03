// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
)

// renderedSyncSkill installs the skills for this repository's prefix and
// returns the rendered mtix-sync skill (MTIX-95.8.1).
func renderedSyncSkill(t *testing.T) string {
	t.Helper()
	data := minimalTemplateData()
	data.ProjectPrefix = "MTIX"
	dir := t.TempDir()
	_, err := NewPluginInstaller(dir, data, nil).Install("claude-code", false)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, ".claude", "skills", "mtix-sync.md")) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	return string(body)
}

// skillBody returns a skill file without its YAML frontmatter, so the
// Claude Code and Codex mirrors, whose frontmatter differs, compare by
// their text.
func skillBody(t *testing.T, skill string) string {
	t.Helper()
	require.True(t, strings.HasPrefix(skill, "---\n"), "the skill starts with frontmatter")
	_, body, ok := strings.Cut(skill[len("---\n"):], "\n---\n")
	require.True(t, ok, "the frontmatter is closed")
	return body
}

// TestPluginInstaller_SyncSkill_MatchesPluginMirror: the sync skill in
// .claude-plugin/skills is byte-identical to the template rendered for
// this repository's prefix, and the Codex plugin's skill carries the same
// text under its own frontmatter (MTIX-95.8.1).
func TestPluginInstaller_SyncSkill_MatchesPluginMirror(t *testing.T) {
	rendered := renderedSyncSkill(t)
	mirror, err := os.ReadFile(filepath.Join("..", "..", ".claude-plugin", "skills", "mtix-sync.md"))
	require.NoError(t, err)
	require.Equal(t, rendered, string(mirror), ".claude-plugin/skills/mtix-sync.md matches the rendered template")

	codex, err := os.ReadFile(filepath.Join("..", "..", ".codex-plugin", "skills", "sync", "SKILL.md"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(codex), "---\nname: sync\ndescription: "), "the Codex skill is named sync")
	require.Equal(t, skillBody(t, rendered), skillBody(t, string(codex)),
		".codex-plugin/skills/sync/SKILL.md carries the rendered skill's text")
}

// TestSyncSkill_RendersSharedHubConnectionPartial: the sync skill includes
// the shared hub_connection partial, and the rendered skill and both
// plugin mirrors carry that partial's text verbatim (MTIX-95.8.1).
func TestSyncSkill_RendersSharedHubConnectionPartial(t *testing.T) {
	tmpl, err := template.ParseFS(embeddedTemplates, "templates/partials/*.tmpl")
	require.NoError(t, err)
	var buf strings.Builder
	require.NoError(t, tmpl.ExecuteTemplate(&buf, "hub_connection", nil))
	partial := strings.TrimSpace(buf.String())

	src, err := os.ReadFile(filepath.Join("templates", "skills", "sync.md.tmpl"))
	require.NoError(t, err)
	require.Contains(t, string(src), `{{ template "hub_connection" . }}`)
	require.Contains(t, renderedSyncSkill(t), partial)
	for _, rel := range []string{".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md"} {
		body, rerr := os.ReadFile(filepath.Join(append([]string{"..", ".."}, strings.Split(rel, "/")...)...))
		require.NoError(t, rerr)
		require.Containsf(t, string(body), partial, "%s carries the hub_connection partial", rel)
	}
}

// TestSyncSkill_Runbook_CoversConfigureUseVerifyRecover: the rendered skill
// has each part of the runbook: the requirements checklist, setup from
// zero, least-privilege roles as SQL, credential sourcing, the
// scale-to-zero cost statement, verification with doctor, the
// troubleshooting map (own-event replay included), recovery, escalation
// and the never-dos (MTIX-95.8.1).
func TestSyncSkill_Runbook_CoversConfigureUseVerifyRecover(t *testing.T) {
	skill := strings.Join(strings.Fields(renderedSyncSkill(t)), " ")
	for _, want := range []string{
		"## Requirements checklist", "## Set up a hub from zero", "## Least-privilege roles",
		"## Credentials", "## Cost and databases that pause when idle", "## Verify with doctor",
		"## Troubleshooting map", "## Recover", "## When to ask the user", "## Never",
		"verify-full", "MTIX_SYNC_SSLROOTCERT", "session-mode", "MTIX_SYNC_DSN", ".mtix/secrets", "mode 0600",
		"CREATE ROLE", "GRANT USAGE ON SCHEMA", "GRANT EXECUTE ON FUNCTION record_restore_collision",
		"mtix sync init", "mtix sync doctor --json", "mtix sync repair --status", "mtix sync backup --output",
		"mtix sync clone", "mtix sync quarantine list",
		"does not poll the hub on a timer", "mtix sync status", "contacts the hub",
		"pull no longer re-applies this client's own events",
		"A role that syncs without owning the sync tables",
	} {
		require.Containsf(t, skill, want, "the skill names %q", want)
	}
	require.NotContains(t, skill, "mtix sync daemon --interval", "no interval daemon is recommended")
}

// TestSyncDocs_NewText_NamesNoProviderAndUsesProfessionalWording: the new
// sync text (the skill, its mirrors and the SYNC section) names no hosting
// provider and avoids the terms the wording guard forbids (MTIX-95.8.1).
func TestSyncDocs_NewText_NamesNoProviderAndUsesProfessionalWording(t *testing.T) {
	texts := map[string]string{}
	for _, rel := range []string{"internal/docs/templates/skills/sync.md.tmpl",
		"internal/docs/templates/partials/sync_section.md.tmpl", ".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md"} {
		body, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, strings.Split(rel, "/")...)...))
		require.NoError(t, err)
		texts[rel] = string(body)
		require.Emptyf(t, offendingTerm(string(body)), "%s uses a forbidden term", rel)
	}
	require.Empty(t, findProviderNames(texts), "the new sync text names a hosting provider")
}
