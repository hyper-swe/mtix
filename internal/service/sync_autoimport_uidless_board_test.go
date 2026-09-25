// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// End-to-end-style tests for MTIX-95.31.9 (FR-15.2i, FR-7.8): a pulled
// board without uids (written by a client at 0.3 or older, schema 1.0.0)
// that holds a task with another title under a local id is never merged
// silently. The automatic import refuses and names the pair ("a task under
// this id with a different title and no uid to compare"); the merge it
// recommends renumbers the local task only with --confirm, keeps it whole
// and names the pair in its report. Written red-first against the
// MTIX-95.31.6 code, which listed only "field uid" and whose merge replaced
// the local title and description.
package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// withoutUIDs turns a board into one a client at 0.3 or older writes:
// schema 1.0.0, no annotations or activity (asOlderClientBoard), and no
// uids.
func withoutUIDs(t *testing.T, board []byte) []byte {
	t.Helper()
	data, err := sqlite.DecodeExportData(bytes.NewReader(asOlderClientBoard(t, board)))
	require.NoError(t, err)
	for i := range data.Nodes {
		data.Nodes[i].UID = ""
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	out, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	return out
}

// TestAutoImport_UIDlessBoardWithAnotherTitle_RefusedAndMergeRenumbers
// verifies the automatic import of a board without uids whose PROJ-2 is
// another task (another title) refuses and names the pair, and not PROJ-1,
// whose title is the same; the recommended merge writes nothing without
// --confirm, reports the renumbering and the pair, and with --confirm the
// board's task takes PROJ-2 while the local PROJ-2 moves to PROJ-3 whole,
// with its uid and description.
func TestAutoImport_UIDlessBoardWithAnotherTitle_RefusedAndMergeRenumbers(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	mine, err := f.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	f.pull(t, withoutUIDs(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		n := &d.Nodes[nodeIndex(t, d, "PROJ-2")]
		n.Title, n.Description, n.ContentHash = "Teammate's own task", "Why: theirs", "h-theirs"
	})))
	before := f.storeSnapshot(t)

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	assert.Equal(t, before, f.storeSnapshot(t))
	msg := f.notices.String()
	assert.Contains(t, msg, `  PROJ-2: a task under this id with a different title and no uid to compare `+
		`(local "Task PROJ-2", file "Teammate's own task")`)
	assert.Contains(t, msg, "a local task listed with a different title and no uid to compare is taken for a "+
		"different task")
	assert.NotContains(t, msg, "PROJ-1: a task under this id")

	report, _, err := f.store.ImportReconcile(ctx, f.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	assert.Equal(t, before, f.storeSnapshot(t), "merge without --confirm writes nothing")
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: mine.UID, OldPath: "PROJ-2", NewPath: "PROJ-3"}}, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportTitleMismatch{{ID: "PROJ-2", LocalTitle: "Task PROJ-2",
		FileTitle: "Teammate's own task"}}, report.TitleMismatches)

	_, _, err = f.store.ImportReconcile(ctx, f.pulledBoard(t), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	theirs, err := f.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "Teammate's own task", theirs.Title)
	kept, err := f.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, mine.Title, kept.Title, "the local task is never overwritten")
	assert.Equal(t, mine.Description, kept.Description)
	assert.Equal(t, mine.UID, kept.UID)
	assert.Equal(t, 2, f.annotationCount(t, "PROJ-1"), "the merge keeps PROJ-1's annotations")
}
