// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// Explicit exports are private; automatic or redirected tracked mirrors are not.
func TestRunExport_ExplicitSnapshot_IncludesPrivateJournalOnlyInExplicitFile(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail one", "mail two")
	var out string
	stderr, err := captureStderrOf(t, func() error {
		var err error
		out, err = cliBehaviorOutput(t, func() error { return runExport("json") })
		return err
	})
	require.NoError(t, err)
	var file sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(out), &file))
	require.Contains(t, out, `"inbox_journal"`)
	require.Contains(t, stderr, "2 addressed inbox event(s)")
	require.NoError(t, app.syncSvc.AutoExport(t.Context(), app.mtixDir))
	mirror, err := os.ReadFile(filepath.Join(app.mtixDir, "tasks.json"))
	require.NoError(t, err)
	require.NotContains(t, string(mirror), `"inbox_journal"`)
}

func TestRunExport_RedirectedMirror_OmitsJournal(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail")
	path := filepath.Join(app.mtixDir, "tasks.json")
	output, err := os.Create(path)
	require.NoError(t, err)
	old := os.Stdout
	t.Cleanup(func() { os.Stdout = old })
	os.Stdout = output
	require.NoError(t, runExport("json"))
	os.Stdout = old
	require.NoError(t, output.Close())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"inbox_journal"`)
	require.Contains(t, string(raw), `"nodes"`)
}

func TestRecoverHelp_PublishedReference_MatchesGenerator(t *testing.T) {
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	generated := generatedHelpReference(t)
	marker := "## recover\n\n**Usage:** `recover`\n"
	actualStart := strings.LastIndex(string(actual), marker)
	generatedStart := strings.LastIndex(generated, marker)
	require.NotEqual(t, -1, actualStart)
	require.NotEqual(t, -1, generatedStart)
	actual = actual[actualStart:]
	generated = generated[generatedStart:]
	require.Equal(t, cliReferenceSection(t, generated, "recover", "recover"), cliReferenceSection(t, string(actual), "recover", "recover"))
}

func TestCheckRecoverExport_UnreadableOrInvalidFile_RejectsInput(t *testing.T) {
	dir := t.TempDir()
	require.ErrorContains(t, checkRecoverExport(filepath.Join(dir, "absent.json")), "open recovery export")
	path := filepath.Join(dir, "invalid.json")
	require.NoError(t, os.WriteFile(path, []byte("{"), 0600))
	require.ErrorContains(t, checkRecoverExport(path), "read recovery export")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":999}`), 0600))
	require.ErrorContains(t, checkRecoverExport(path), "validate recovery export")
}

func TestExportIncludesInbox_MirrorAlias_OmitsJournal(t *testing.T) {
	dir := t.TempDir()
	mirror := filepath.Join(dir, "tasks.json")
	require.NoError(t, os.WriteFile(mirror, []byte("{}"), 0600))
	for _, kind := range []string{"symlink", "hardlink"} {
		alias := filepath.Join(dir, kind)
		if kind == "symlink" {
			require.NoError(t, os.Symlink(mirror, alias))
		} else {
			require.NoError(t, os.Link(mirror, alias))
		}
		out, err := os.OpenFile(alias, os.O_WRONLY, 0600)
		require.NoError(t, err)
		includes, err := exportIncludesInbox(out, dir)
		require.NoError(t, err)
		require.False(t, includes)
		require.NoError(t, out.Close())
		_, err = exportIncludesInbox(out, dir)
		require.ErrorContains(t, err, "inspect export output")
	}
}

func TestExportIncludesInbox_UnreadableMirrorIdentity_RefusesExport(t *testing.T) {
	dir := t.TempDir()
	mirror := filepath.Join(dir, "tasks.json")
	require.NoError(t, os.Symlink(mirror, mirror))
	out, err := os.Create(filepath.Join(dir, "private.json"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, out.Close()) })
	_, err = exportIncludesInbox(out, dir)
	require.ErrorContains(t, err, "inspect tracked export mirror")
}
