// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func goldenWorkflowConflict(id, choice string) WorkflowConflict {
	old, now := "alice", "bob <&> \"q\""
	hist := []exportHistoryEntry{{ID: "01H1", TS: "2026-01-01T00:00:00Z", Actor: "a\nb", Origin: "cli", Kind: "set", Old: &old, New: &now}}
	return WorkflowConflict{ID: id, Title: "T \"" + id + "\"", Hint: "both\x1b[31m sides", Choice: WorkflowChoice(choice),
		Fields: []WorkflowFieldDiff{
			{Field: "status", Local: "open", File: "", LocalHistory: hist, LocalHistoryMore: 3},
			{Field: "assignee", Local: "", File: "bob"},
		}}
}

// goldenReports are reports of every shape: empty, every list filled, a
// repeated uid (the last entry wins in the remap), minted uids, titles that differ.
func goldenReports() map[string]*ImportReconcileReport {
	full := func(applied bool) *ImportReconcileReport {
		return &ImportReconcileReport{
			Applied: applied, Idempotent: 7,
			Conflicts: []ImportUIDConflict{
				{UID: "u1", ImportPath: "A-1", LocalPath: "A-2", Kind: ConflictLocalUIDMismatch},
				{UID: "u2", ImportPath: "A-3", LocalPath: "A-4", Kind: ConflictExportDuplicateUID}},
			Remaps:         []ImportRemapEntry{{UID: "p1", OldPath: "A-1.x", NewPath: "A-1.1"}, {UID: "", OldPath: "A-1.y", NewPath: "A-1.2"}},
			Renamed:        []ImportRemapEntry{{UID: "r1", OldPath: "A-5", NewPath: "A-5"}},
			LocalRenumbers: []ImportRemapEntry{{UID: "l1", OldPath: "A-2", NewPath: "A-6"}, {UID: "m1", OldPath: "A-7", NewPath: "A-8", NewUID: true}, {UID: "p1", OldPath: "A-9", NewPath: "A-10"}},
			Moved:          []ImportRemapEntry{{UID: "v1", OldPath: "A-11", NewPath: "A-12"}},
			UIDAdoptions: []ImportUIDAdoption{{ID: "A-1", LocalUID: "lo", FileUID: "fi", LocalTitle: "x", FileTitle: "y \"z\""},
				{ID: "A-2", LocalUID: "", FileUID: "fi2", LocalTitle: "same", FileTitle: "same"}},
			TitleMismatches:   []ImportTitleMismatch{{ID: "A-2", LocalTitle: "x\ty", FileTitle: "é"}},
			WorkflowConflicts: []WorkflowConflict{goldenWorkflowConflict("A-1", ""), goldenWorkflowConflict("A-2", "theirs")},
		}
	}
	onlyNew := &ImportReconcileReport{Applied: false, LocalRenumbers: []ImportRemapEntry{{UID: "m", OldPath: "A-1", NewPath: "A-2", NewUID: true}}}
	onlyEmptyUID := &ImportReconcileReport{Moved: []ImportRemapEntry{{OldPath: "A-1", NewPath: "A-2"}}}
	return map[string]*ImportReconcileReport{
		"empty": {}, "full unapplied": full(false), "full applied": full(true), "only minted uid": onlyNew,
		"only a uid-less move": onlyEmptyUID,
	}
}

// The report text, the remap file and the --json document are what they were
// before the formatter became a stream, for every shape of report.
func TestReportFormatter_EqualsLegacyOutput(t *testing.T) {
	for name, report := range goldenReports() {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, legacyString(report), report.String())
			var text bytes.Buffer
			require.NoError(t, report.WriteReport(&text))
			assert.Equal(t, legacyString(report), text.String())

			want, err := legacyRemap(report)
			require.NoError(t, err)
			has, err := report.HasRemap()
			require.NoError(t, err)
			assert.Equal(t, want != "", has, "a remap file is written exactly when it was")
			if has {
				var remap bytes.Buffer
				require.NoError(t, report.WriteRemap(&remap))
				assert.Equal(t, want, remap.String())
			}
			wantJSON, err := legacyWorkflowJSON(report)
			require.NoError(t, err)
			var doc bytes.Buffer
			require.NoError(t, report.WriteWorkflowJSON(&doc))
			assert.Equal(t, wantJSON, doc.String())
		})
	}
}

// A writer that fails stops the report with its error, and nothing hides it.
func TestReportFormatter_WriteErrorIsReturned(t *testing.T) {
	report := goldenReports()["full unapplied"]
	require.ErrorIs(t, report.WriteReport(failingWriter{}), errWriteFailed)
	require.ErrorIs(t, report.WriteRemap(failingWriter{}), errWriteFailed)
	require.ErrorIs(t, report.WriteWorkflowJSON(failingWriter{}), errWriteFailed)
}

var errWriteFailed = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }
