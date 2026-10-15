// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Root tests keep diagnostics and the printed repair command on the same files.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoctorDocs_NestedDirectory_ChecksBoardRootAndPrintsRemedy(t *testing.T) {
	root := doctorCLIBoard(t, "")
	out := doctorCLIExecute(t, root, "", "docs", "generate")
	require.Zero(t, out.exit, out.stderr)
	nested := filepath.Join(root, "nested folder")
	require.NoError(t, os.Mkdir(nested, 0700))
	doctorDocsWrite(t, nested, "MTIX.md", "unrelated nested document")
	before := doctorDocsSnapshot(t, nested)
	out = doctorCLIExecute(t, nested, "", "doctor", "--json")
	require.Zero(t, out.exit, out.stderr)
	assert.Equal(t, "pass", doctorDocsRow(t, out.stdout)["status"])
	doctorDocsWrite(t, root, "MTIX.md", "stale")
	out = doctorCLIExecute(t, nested, "", "doctor", "--json")
	require.Zero(t, out.exit, out.stderr)
	row := doctorDocsRow(t, out.stdout)
	assert.Equal(t, "warn", row["status"])
	canonicalRoot, resolveErr := filepath.EvalSymlinks(root)
	require.NoError(t, resolveErr)
	assert.Equal(t, "mtix -C '"+canonicalRoot+"' docs generate", row["fix"])
	out = doctorCLIExecute(t, nested, "", "doctor")
	require.Zero(t, out.exit, out.stderr)
	assert.Contains(t, out.stdout, "[WARN] docs.stale")
	assert.Contains(t, out.stdout, row["fix"])
	out = doctorCLIExecute(t, root, "", "docs", "generate")
	require.Zero(t, out.exit, out.stderr)
	out = doctorCLIExecute(t, nested, "", "doctor", "--json")
	require.Zero(t, out.exit, out.stderr)
	assert.Equal(t, "pass", doctorDocsRow(t, out.stdout)["status"])
	assert.Equal(t, before, doctorDocsSnapshot(t, nested))
	require.NoDirExists(t, filepath.Join(nested, ".mtix"))
}

func TestDoctorDocs_RootWithQuote_EmitsQuotedCommandAndNoContents(t *testing.T) {
	saveAndResetApp(t)
	root := filepath.Join(t.TempDir(), "a board's root")
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".mtix"), 0700))
	row := documentFreshnessCheck(root).Run(t.Context())
	assert.Equal(t, "warn", string(row.Status))
	want := "mtix -C '" + strings.ReplaceAll(root, "'", "'\"'\"'") + "' docs generate"
	assert.Equal(t, want, row.Fix)
}

func TestDoctorDocs_NonregularInput_WarnsWithoutWriting(t *testing.T) {
	root := doctorCLIBoard(t, "")
	out := doctorCLIExecute(t, root, "", "docs", "generate")
	require.Zero(t, out.exit, out.stderr)
	require.NoError(t, os.Remove(filepath.Join(root, "CLAUDE.md")))
	require.NoError(t, os.Mkdir(filepath.Join(root, "CLAUDE.md"), 0700))
	out = doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
	require.Zero(t, out.exit, out.stderr)
	row := doctorDocsRow(t, out.stdout)
	assert.Equal(t, "warn", row["status"])
	assert.Contains(t, row["detail"], "could not be inspected")
	require.DirExists(t, filepath.Join(root, "CLAUDE.md"))
}

func TestDoctorDocs_GitPolicy_ReadOnlyEvenWithFix(t *testing.T) {
	root := doctorCLIBoard(t, "")
	briefingGit(t, root, "init", "-q")
	out := doctorCLIExecute(t, root, "", "docs", "generate")
	require.Zero(t, out.exit, out.stderr)
	doctorDocsWrite(t, root, ".gitignore", "/CLAUDE.md\n/AGENTS.md\n")
	doctorDocsWrite(t, root, ".git/info/exclude", "# user policy\r\n/local-file\r\n")
	before := doctorDocsSnapshot(t, root)
	for _, flags := range [][]string{{"doctor", "--json"}, {"doctor", "--json", "--fix"}} {
		out = doctorCLIExecute(t, root, "", flags...)
		require.Zero(t, out.exit, out.stderr)
		assert.Equal(t, "pass", doctorDocsRow(t, out.stdout)["status"])
		assert.Equal(t, before, doctorDocsSnapshot(t, root))
	}
}
