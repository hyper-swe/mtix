// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// syncSectionRE captures the managed SYNC section of a generated document.
var syncSectionRE = regexp.MustCompile(`(?s)<!-- AUTO-GENERATED: SYNC -->\n(.*?)\n<!-- END AUTO-GENERATED -->`)

// generatedSyncSections generates the docs for data and returns the managed
// SYNC section of CLAUDE.md and AGENTS.md, by file name (MTIX-95.8.1).
func generatedSyncSections(t *testing.T, data *TemplateData) map[string]string {
	t.Helper()
	dir := t.TempDir()
	gen, err := NewEmbeddedGenerator(dir, data, nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)
	out := map[string]string{}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md"} {
		body, rerr := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // path from t.TempDir()
		require.NoError(t, rerr)
		m := syncSectionRE.FindStringSubmatch(string(body))
		require.NotNilf(t, m, "%s carries a managed SYNC section", name)
		out[name] = m[1]
	}
	return out
}

// contentLines counts the non-blank lines of a section.
func contentLines(section string) int {
	n := 0
	for _, l := range strings.Split(section, "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// TestDocsGenerate_SyncSectionWhenHubConfigured: with a hub DSN source
// configured, CLAUDE.md and AGENTS.md carry the same managed SYNC section
// of at most 15 lines: what the hub is, push then pull at session start,
// push after changes, MTIX_SYNC_HOOK=1 for hooks, mtix sync status, the
// never-dos, and the pointer to the mtix-sync skill (MTIX-95.8.1).
func TestDocsGenerate_SyncSectionWhenHubConfigured(t *testing.T) {
	data := minimalTemplateData()
	data.SyncHubConfigured = true
	sections := generatedSyncSections(t, data)
	require.Equal(t, sections["CLAUDE.md"], sections["AGENTS.md"], "AGENTS.md matches CLAUDE.md")
	for name, section := range sections {
		require.LessOrEqualf(t, contentLines(section), 15, "%s: the SYNC section is at most 15 lines", name)
		for _, want := range []string{
			"shared hub", "MTIX_SYNC_DSN", ".mtix/secrets", "`mtix sync push`", "`mtix sync pull`",
			"`mtix sync status`", "MTIX_SYNC_HOOK=1", "Never", "`mtix sync reconcile --discard-local`",
			"scale-to-zero", "`mtix-sync` skill",
		} {
			require.Containsf(t, section, want, "%s: the SYNC section names %q", name, want)
		}
		require.Less(t, strings.Index(section, "`mtix sync push`"), strings.Index(section, "`mtix sync pull`"),
			"%s: push comes before pull", name)
		require.Regexp(t, `(?i)push.{0,40}then.{0,20}pull|push first`, section, "%s: says why push is first", name)
	}
}

// TestDocsGenerate_SyncSectionPointerOtherwise: with no hub DSN source, the
// managed SYNC section is one line that points at the mtix-sync skill and
// holds no push or pull routine (MTIX-95.8.1).
func TestDocsGenerate_SyncSectionPointerOtherwise(t *testing.T) {
	data := minimalTemplateData()
	data.SyncHubConfigured = false
	for name, section := range generatedSyncSections(t, data) {
		require.Equalf(t, 1, contentLines(section), "%s: the pointer is one line", name)
		require.Contains(t, section, "`mtix-sync` skill", name)
		require.NotContains(t, section, "mtix sync push", name)
		require.NotContains(t, section, "MTIX_SYNC_HOOK", name)
	}
}
