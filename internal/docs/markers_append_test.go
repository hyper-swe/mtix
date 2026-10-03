// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGenerate_ForceOnFileWithOtherMarkers_AppendsMissingSyncSection: an
// existing AGENTS.md that carries only the MCP_TOOLS marker gains the SYNC
// section on a forced generate, every user line survives, and a second run
// changes nothing (MTIX-95.8.1).
func TestGenerate_ForceOnFileWithOtherMarkers_AppendsMissingSyncSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	existing := "# My agents file\n\nuser line one\n\n<!-- AUTO-GENERATED: MCP_TOOLS -->\nold tools\n<!-- END AUTO-GENERATED -->\n\nuser line two\n"
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o644))

	data := minimalTemplateData()
	data.SyncHubConfigured = true
	gen, err := NewEmbeddedGenerator(dir, data, nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)

	first, err := os.ReadFile(path) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	body := string(first)
	require.Contains(t, body, "<!-- AUTO-GENERATED: SYNC -->")
	require.Contains(t, body, "Session start: run `mtix sync push`")
	require.Equal(t, 1, strings.Count(body, "<!-- AUTO-GENERATED: SYNC -->"))
	for _, keep := range []string{"# My agents file", "user line one", "user line two"} {
		require.Contains(t, body, keep)
	}
	require.NotContains(t, body, "old tools", "the existing marked section is refreshed")

	_, err = gen.Generate(true)
	require.NoError(t, err)
	second, err := os.ReadFile(path) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	require.Equal(t, body, string(second), "a second run is idempotent")
}

// TestGenerate_ForceOnFileWithUnterminatedMarker_KeepsEveryByte: a file with
// an unterminated SYNC marker next to a complete marker is left untouched
// by two forced runs, so no user text between markers is lost (MTIX-95.8.1).
func TestGenerate_ForceOnFileWithUnterminatedMarker_KeepsEveryByte(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	malformed := "# Mine\n\n<!-- AUTO-GENERATED: SYNC -->\nuser notes inside an unterminated block\n\n" +
		"<!-- AUTO-GENERATED: MCP_TOOLS -->\nold\n<!-- END AUTO-GENERATED -->\n\nmore user text\n"
	require.NoError(t, os.WriteFile(path, []byte(malformed), 0o644))

	data := minimalTemplateData()
	data.SyncHubConfigured = true
	gen, err := NewEmbeddedGenerator(dir, data, nil)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = gen.Generate(true)
		require.NoError(t, err)
		got, rerr := os.ReadFile(path) //nolint:gosec // path from t.TempDir()
		require.NoError(t, rerr)
		require.Equal(t, malformed, string(got), "run %d keeps the file byte for byte", i+1)
	}
}
