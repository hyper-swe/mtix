// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// File error tests verify refusal before opening unsupported root paths.
package docs

import (
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
)

func TestProjectBriefing_InvalidTemplate_ReturnsErrorBeforeWrites(t *testing.T) {
	cases := []struct {
		name string
		tmpl *template.Template
		data *TemplateData
	}{
		{"missing templates", nil, minimalTemplateData()},
		{"missing data", template.New("unused"), nil},
		{"missing agent template", template.New("unused"), minimalTemplateData()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := generateProjectBriefing(dir, tc.tmpl, tc.data)
			require.Error(t, err)
			require.NoFileExists(t, filepath.Join(dir, "MTIX.md"))
		})
	}
}

func TestProjectFiles_InvalidPaths_ReturnErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, []byte("safe"), 0600))
	cases := []struct{ name, path string }{
		{"directory", dir},
		{"file parent", filepath.Join(file, "child")},
		{"missing parent", filepath.Join(dir, "missing", "child")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, writeRegularDoc(tc.path, []byte("new")))
			_, _, err := readRegularDoc(tc.path)
			if tc.name != "missing parent" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestProjectFiles_Overwrite_ShrinksWithoutChangingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc")
	require.NoError(t, os.WriteFile(path, []byte("long original document"), 0600))
	require.NoError(t, writeRegularDoc(path, []byte("new")))
	data, exists, err := readRegularDoc(path)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, "new", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestGenerator_ProjectRoot_MalformedPairPropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	require.NoError(t, os.WriteFile(path, []byte("<!-- AUTO-GENERATED: SYNC -->"), 0600))
	gen, err := NewEmbeddedGenerator(filepath.Join(dir, ".mtix", "docs"), minimalTemplateData(), nil)
	require.NoError(t, err)
	gen.SetProjectRoot(dir)
	_, err = gen.Generate(false)
	require.ErrorContains(t, err, "marker")
	require.NoFileExists(t, filepath.Join(dir, "MTIX.md"))
}

func TestUpdateProjectExclusion_ExistingEntry_IsIdempotent(t *testing.T) {
	for _, entry := range []string{"/MTIX.md\n", "MTIX.md\n", "# custom without newline"} {
		t.Run(entry, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "exclude")
			require.NoError(t, os.WriteFile(path, []byte(entry), 0600))
			require.NoError(t, updateProjectExclusion(path, true))
			require.NoError(t, updateProjectExclusion(path, true))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, entry+projectExclusionBlock, string(data))
			require.NoError(t, updateProjectExclusion(path, false))
			restored, restoreErr := os.ReadFile(path)
			require.NoError(t, restoreErr)
			require.Equal(t, entry, string(restored))

		})
	}
}

func TestUpdateProjectExclusion_NonregularFile_Refuses(t *testing.T) {
	require.ErrorContains(t, updateProjectExclusion(t.TempDir(), true), "not a regular file")
}
