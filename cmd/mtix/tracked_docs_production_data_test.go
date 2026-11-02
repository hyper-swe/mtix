// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/mcp"
)

var managedSectionRE = regexp.MustCompile(`(?s)<!-- AUTO-GENERATED: (\w+) -->(.*?)<!-- END AUTO-GENERATED -->`)

// managedSections maps each AUTO-GENERATED section name to its trimmed body.
func managedSections(content string) map[string]string {
	out := map[string]string{}
	for _, m := range managedSectionRE.FindAllStringSubmatch(content, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

// TestTrackedDocs_ManagedSectionsMatchProductionRender guards MTIX-235: it
// renders every generated document with the production template data (the
// real command tree, config keys, error codes and MCP registry) and requires
// every managed section to equal the tracked copy under docs/. This covers
// the sections that depend on that data, which the internal/docs guard cannot
// fill. The tracked docs carry the MCP registry empty, as mtix docs generate
// renders it. docs/codex/AGENTS.md holds no managed sections and is hand
// maintained, so it has nothing to compare.
func TestTrackedDocs_ManagedSectionsMatchProductionRender(t *testing.T) {
	data := docs.BuildTemplateData(newRootCmd(), mcp.NewToolRegistry(), "PROJ", "0.5.4-beta")
	out := t.TempDir()
	gen, err := docs.NewEmbeddedGenerator(out, data, nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)

	for _, doc := range docs.AllDocFiles() {
		t.Run(doc.Name, func(t *testing.T) {
			tracked, err := os.ReadFile(filepath.Join("..", "..", "docs", doc.Name))
			if os.IsNotExist(err) {
				t.Skipf("docs/%s is not tracked (recorded in internal/docs untrackedGeneratedDocs)", doc.Name)
			}
			require.NoError(t, err)
			rendered, err := os.ReadFile(filepath.Join(out, doc.Name))
			require.NoError(t, err)
			want, got := managedSections(string(rendered)), managedSections(string(tracked))
			names := make([]string, 0, len(want))
			for name := range want {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				assert.Equal(t, want[name], got[name],
					"docs/%s section %s drifted from the production render; regenerate: run mtix docs generate in a scratch project and copy the section from .mtix/docs/%s", doc.Name, name, doc.Name)
			}
		})
	}
}
