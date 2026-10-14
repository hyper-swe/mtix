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

func doctorExistingMarkedGuides(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		custom := "# Local " + name + "\noperator custom text\n<!-- AUTO-GENERATED: HOOK_APPROVALS -->\nold approvals\n<!-- END AUTO-GENERATED -->\nlocal footer\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(custom), 0o600))
	}
	return dir
}

func doctorAssertRefreshedGuide(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "# Local "+name)
	assert.Contains(t, text, "operator custom text")
	assert.Contains(t, text, "local footer")
	assert.NotContains(t, text, "old approvals")
	assert.Contains(t, extractSections(text)["HOOK_APPROVALS"], "mtix hooks trust <hook>")
	assert.Contains(t, text, "mtix doctor --json")
	assert.Contains(t, text, "mtix doctor --fix")
	assert.Equal(t, 1, strings.Count(text, "mtix doctor --json"))
	assert.Equal(t, 1, strings.Count(text, "mtix doctor --fix"))
	return raw
}

func TestDoctorGuidance_ForceRefreshExistingManagedGuides_IsIdempotent(t *testing.T) {
	dir := doctorExistingMarkedGuides(t)
	gen, err := NewEmbeddedGenerator(dir, minimalTemplateData(), nil)
	require.NoError(t, err)
	_, err = gen.Generate(true)
	require.NoError(t, err)
	first := make(map[string][]byte)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		first[name] = doctorAssertRefreshedGuide(t, dir, name)
	}
	_, err = gen.Generate(true)
	require.NoError(t, err)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		assert.Equal(t, first[name], doctorAssertRefreshedGuide(t, dir, name), "forced refresh remains idempotent")
	}
}
