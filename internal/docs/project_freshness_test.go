// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Freshness tests use the same renderer as generation without creating repairs.
package docs

import (
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freshnessFixture(t *testing.T) (string, *Generator) {
	t.Helper()
	root := t.TempDir()
	g, err := NewEmbeddedGenerator(filepath.Join(root, ".mtix", "docs"), minimalTemplateData(), nil)
	require.NoError(t, err)
	g.SetProjectRoot(root)
	_, err = g.Generate(false)
	require.NoError(t, err)
	return root, g
}

func TestCheckProjectBriefing_GenerationParity_DetectsChanges(t *testing.T) {
	for _, tc := range []struct {
		name, file, text string
		want             string
	}{
		{"fresh", "", "", ""},
		{"stale", "MTIX.md", "changed", "MTIX.md differs"},
		{"missing-pointer", "CLAUDE.md", "user text\n", "CLAUDE.md has no live"},
		{"duplicate", "AGENTS.md", agentsPointer + "\nSee MTIX.md for mtix instructions.\n", "AGENTS.md has duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, g := freshnessFixture(t)
			if tc.file != "" {
				require.NoError(t, os.WriteFile(filepath.Join(root, tc.file), []byte(tc.text), 0600))
			}
			reasons, err := g.CheckProjectBriefing(root)
			require.NoError(t, err)
			if tc.want == "" {
				assert.Empty(t, reasons)
			} else {
				require.Len(t, reasons, 1)
				assert.Contains(t, reasons[0], tc.want)
				_, err = g.Generate(false)
				require.NoError(t, err)
				reasons, err = g.CheckProjectBriefing(root)
				require.NoError(t, err)
				assert.Empty(t, reasons, "the printed generation remedy clears the finding")
			}
		})
	}
}

func TestCheckProjectBriefing_InvalidInputs_ReturnsErrors(t *testing.T) {
	for _, name := range []string{"nil-data", "nil-templates", "broken-template", "missing-root", "output-file", "MTIX.md", "CLAUDE.md", "AGENTS.md"} {
		t.Run(name, func(t *testing.T) {
			root, g := freshnessFixture(t)
			switch name {
			case "nil-data":
				g.data = nil
			case "nil-templates":
				g.templates = nil
			case "broken-template":
				g.templates = template.New("missing embedded templates")
			case "missing-root":
				root = filepath.Join(root, "absent")
			case "output-file":
				g.outputDir = filepath.Join(root, "file")
				require.NoError(t, os.WriteFile(g.outputDir, nil, 0600))
			default:
				path := filepath.Join(root, name)
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0700))
			}
			_, err := g.CheckProjectBriefing(root)
			require.Error(t, err)
		})
	}
}

func TestGenerator_HypersweDuplicates_PreservesFirstAndClearsFinding(t *testing.T) {
	root, g := freshnessFixture(t)
	for name, body := range map[string]string{
		"CLAUDE.md":   "user\r\n  @MTIX.md\r\n@MTIX.md\r\ntail",
		"AGENTS.md":   "See MTIX.md for mtix instructions.\nuser\n" + agentsPointer,
		"HYPERSWE.md": "owner bytes",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(body), 0600))
	}
	reasons, err := g.CheckProjectBriefing(root)
	require.NoError(t, err)
	require.Len(t, reasons, 2)
	for range 2 {
		_, err = g.Generate(false)
		require.NoError(t, err)
		reasons, err = g.CheckProjectBriefing(root)
		require.NoError(t, err)
		assert.Empty(t, reasons)
		for name, want := range map[string]string{
			"CLAUDE.md":   "user\r\n  @MTIX.md\r\ntail",
			"AGENTS.md":   "See MTIX.md for mtix instructions.\nuser\n",
			"HYPERSWE.md": "owner bytes",
		} {
			body, readErr := os.ReadFile(filepath.Join(root, name))
			require.NoError(t, readErr)
			assert.Equal(t, want, string(body))
		}
	}
}
