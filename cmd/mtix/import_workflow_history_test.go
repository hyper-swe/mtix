// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Exercise the actual import output contract for a refused workflow merge.
package main

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRunImport_WorkflowHistory_TextAndJSON(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[asJSON], func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Shared task", "", "", 3, "", "", "", "", ""))
			app.store.SetHistoryExportSource(func() bool { return true })
			alice := "alice"
			require.NoError(t, app.store.UpdateNode(t.Context(), "TEST-1", &store.NodeUpdate{Assignee: &alice}))
			board := workflowBoard(t, func(data *sqlite.ExportData) {
				n := &data.Nodes[0]
				n.Assignee = "bob"
				for _, e := range n.History {
					if e.Field == "assignee" {
						e.ID, e.Actor = "01W000000000000000000000001", "peer-worker"
						n.History = append(n.History, e)
						break
					}
				}
			})
			carol := "carol"
			require.NoError(t, app.store.UpdateNode(t.Context(), "TEST-1", &store.NodeUpdate{Assignee: &carol}))
			app.jsonOutput = asJSON
			var stdout string
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() { require.Error(t, runImport(board, importFlags{mode: "merge"})) })
			})
			assert.Contains(t, stderr, "yours history")
			assert.Contains(t, stderr, "peer-worker")
			if asJSON {
				var result struct {
					Applied   bool `json:"applied"`
					Conflicts []struct {
						Fields []struct {
							Local []any `json:"local_history"`
							File  []any `json:"file_history"`
						} `json:"fields"`
					} `json:"workflow_conflicts"`
				}
				require.NoError(t, json.Unmarshal([]byte(stdout), &result))
				assert.False(t, result.Applied)
				require.Len(t, result.Conflicts, 1)
				require.Len(t, result.Conflicts[0].Fields, 1)
				assert.Len(t, result.Conflicts[0].Fields[0].Local, 1)
				assert.Len(t, result.Conflicts[0].Fields[0].File, 1)
			} else {
				assert.Empty(t, stdout)
			}
			assert.Empty(t, preSyncBackups(t), "a refused merge takes no backup")
		})
	}
}

func TestWriteImportReport_OutputErrorsPreserveAppliedState(t *testing.T) {
	require.NoError(t, writeImportReport(nil, ""))
	for _, tc := range []struct {
		name    string
		applied bool
		sink    string
	}{
		{"refused stderr", false, "stderr"}, {"applied stderr", true, "stderr"},
		{"refused JSON", false, "stdout"}, {"refused remap", false, "remap"}, {"applied remap", true, "remap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initTestApp(t)
			report := &sqlite.ImportReconcileReport{Applied: tc.applied, WorkflowConflicts: []sqlite.WorkflowConflict{{ID: "TEST-1"}}, Remaps: []sqlite.ImportRemapEntry{{UID: "fixture", NewPath: "TEST-2"}}}
			closed, err := os.CreateTemp(t.TempDir(), "closed-output")
			require.NoError(t, err)
			require.NoError(t, closed.Close())
			originalOut, originalErr := os.Stdout, os.Stderr
			defer func() { os.Stdout, os.Stderr = originalOut, originalErr }()
			remap := ""
			switch tc.sink {
			case "stdout":
				app.jsonOutput, os.Stdout = true, closed
			case "stderr":
				os.Stderr = closed
			case "remap":
				remap = t.TempDir()
			}
			err = writeImportReport(report, remap)
			require.Error(t, err)
			var unchanged *nothingWrittenError
			assert.Equal(t, !tc.applied, errors.As(err, &unchanged), "an applied import must never be labelled unchanged")
		})
	}
}
