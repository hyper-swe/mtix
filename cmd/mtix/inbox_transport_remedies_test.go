// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"database/sql"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRecover_ExistingProjectProtectsPrivateFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("# existing project\nbuild/\n"), 0600))
	require.NoError(t, exec.Command("git", "init", "--quiet", root).Run())
	path, err := writeSalvageFile(dir, []byte(`{"private":"mail body"}`))
	require.NoError(t, err)
	require.NoError(t, exec.Command("git", "-C", root, "add", "--all").Run())
	staged, err := exec.Command("git", "-C", root, "ls-files").Output()
	require.NoError(t, err)
	require.NotContains(t, string(staged), filepath.Base(path))
	ignored, err := exec.Command("git", "-C", root, "check-ignore", "--", path).Output()
	require.NoError(t, err)
	require.Contains(t, string(ignored), filepath.Base(path))
	content, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(content), "# existing project\nbuild/\n")
}

func TestRestoredInbox_ReplaceAndDoctorPrintSafeRecovery(t *testing.T) {
	for _, action := range []string{"replace", "doctor"} {
		t.Run(action, func(t *testing.T) {
			source, file := restoredInboxSource(t)
			initTestApp(t)
			_, err := app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			events, err := source.ReadPendingEvents(t.Context(), 100)
			require.NoError(t, err)
			require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error { return app.store.IdempotentApply(t.Context(), tx, events[0]) }))
			var text string
			if action == "replace" {
				err = refuseSyncedReplace(t.Context())
				require.Error(t, err)
				text = err.Error()
			} else {
				_, err = app.store.WriteDB().Exec(`DELETE FROM nodes`)
				require.NoError(t, err)
				report := appendLoggedNodesCheck(t.Context(), DoctorReport{}, app.store)
				for _, check := range report.Checks {
					if check.Name == loggedNodesCheckName {
						text = check.Fix
					}
				}
			}
			require.Contains(t, text, "restored mail")
			require.Contains(t, text, "new empty directory")
			require.Contains(t, text, "mtix sync clone")
			require.NotContains(t, text, "--discard-local")
			mail, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, mail, 1)
		})
	}
}

// A partial journal must never be presented as a complete salvage (FR-19.1).
func TestRecoverReport_IncompleteJournal_DoesNotSayComplete(t *testing.T) {
	result := &sqlite.RecoverResult{Export: &sqlite.ExportData{NodeCount: 1}, InboxIncomplete: true, Notes: []string{"journal incomplete: error-probe limit 10000 reached at rowid 10009; lost record count unknown"}}
	output := captureStdout(t, func() { printRecoverReport(result, "private-recovered.json") })
	require.Contains(t, output, "Partial salvage (inbox journal incomplete)")
	require.NotContains(t, output, "Salvage complete")
	require.Contains(t, output, "error-probe limit 10000 reached at rowid 10009")
	require.Contains(t, output, "lost record count unknown")
}
