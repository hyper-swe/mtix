// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// untrackedGeneratedDocs lists generated documents that the repository does
// not track under docs/. Each entry is a recorded absence, not a skip: a
// generated document that is neither tracked nor listed here fails the guard.
var untrackedGeneratedDocs = map[string]string{
	"CLAUDE.md": "rendered per project by mtix docs generate; claude.md.tmpl output is not tracked in docs/",
}

// docs/codex/AGENTS.md is tracked but is not a generated document: it is
// hand maintained, holds no AUTO-GENERATED markers and has no template.
// Sections that depend on the command tree, config keys, error codes or MCP
// registry are compared by the production-data guard in cmd/mtix.

// TestTemplates_ClaudeAndAgentsSharedManagedSections_RenderIdentically covers
// claude.md.tmpl, whose output is not tracked: the managed sections it shares
// with agents.md.tmpl must render the same body, so the tracked AGENTS.md
// comparison guards both.
func TestTemplates_ClaudeAndAgentsSharedManagedSections_RenderIdentically(t *testing.T) {
	out := renderAllDocs(t, minimalTemplateData())
	read := func(name string) map[string]string {
		b, err := os.ReadFile(filepath.Join(out, name))
		require.NoError(t, err)
		return extractSections(string(b))
	}
	agents, claude := read("AGENTS.md"), read("CLAUDE.md")
	shared := 0
	for name, body := range claude {
		other, ok := agents[name]
		if !ok {
			continue
		}
		shared++
		assert.Equal(t, other, body, "managed section %s differs between claude.md.tmpl and agents.md.tmpl", name)
	}
	require.NotZero(t, shared, "claude.md.tmpl and agents.md.tmpl share no managed sections")
}

// TestTrackedDocs_ManagedSections_MatchTemplates guards MTIX-235: every
// AUTO-GENERATED section that a template renders must equal the same section
// of the tracked copy under docs/, so a template change cannot ship without
// the regenerated tracked document. Failures name the document, the section
// and the regenerate command.
func TestTrackedDocs_ManagedSections_MatchTemplates(t *testing.T) {
	out := renderAllDocs(t, minimalTemplateData())
	other := renderAllDocs(t, alternateTemplateData())

	for _, doc := range AllDocFiles() {
		t.Run(doc.Name, func(t *testing.T) {
			rendered, err := os.ReadFile(filepath.Join(out, doc.Name))
			require.NoError(t, err)
			want := extractSections(string(rendered))
			alt, err := os.ReadFile(filepath.Join(other, doc.Name))
			require.NoError(t, err)
			altSections := extractSections(string(alt))
			tracked, err := os.ReadFile(filepath.Join("..", "..", "docs", doc.Name))
			if os.IsNotExist(err) {
				_, listed := untrackedGeneratedDocs[doc.Name]
				require.True(t, listed, "docs/%s is not tracked and not listed in untrackedGeneratedDocs", doc.Name)
				return
			}
			require.NoError(t, err)
			got := extractSections(string(tracked))
			names := make([]string, 0, len(want))
			for name := range want {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if altSections[name] != want[name] {
					// The section renders from the live command tree or
					// config registry, so only the real binary can fill it.
					t.Logf("section %s is data dependent; not compared", name)
					continue
				}
				assert.Equal(t, want[name], got[name],
					"docs/%s section %s drifted from its template; regenerate with: mtix docs generate (managed sections refresh in place)",
					doc.Name, name)
			}
		})
	}
}

// renderAllDocs generates every document with data into a fresh directory.
func renderAllDocs(t *testing.T, data *TemplateData) string {
	t.Helper()
	dir := t.TempDir()
	gen, err := NewEmbeddedGenerator(dir, data, nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)
	return dir
}

// alternateTemplateData differs from minimalTemplateData in every field a
// template reads, so a section equal under both is independent of the data.
func alternateTemplateData() *TemplateData {
	data := minimalTemplateData()
	data.ProjectPrefix = "ALT"
	data.Version = "9.9.9-alt"
	data.Commands = append(data.Commands, CommandInfo{Name: "alt", Use: "alt", Short: "Alternate command"})
	data.MCPTools = append(data.MCPTools, MCPToolInfo{Name: "mtix_alt", Description: "Alternate tool"})
	data.ConfigKeys = append(data.ConfigKeys, "alt.key")
	data.ErrorCodes = append(data.ErrorCodes, "ErrAlt")
	return data
}
