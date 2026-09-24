// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Two-clone tests for MTIX-95.31.4 (FR-15.2i): when this store and a pulled
// .mtix/tasks.json hold DIFFERENT tasks under one id (two clones created
// PROJ-3 independently), the auto-import refuses, naming "a different task
// under this id", and the merge it recommends never overwrites the local
// task: it renumbers it to the next number free in both, keeping its uid,
// and applies only with confirmation. Written red-first against the
// MTIX-95.31.2 round-3 code.
package service_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// createLocalTask creates a root task under the number the local sequence
// counter hands out next, as mtix create does, with a description and an
// annotation of its own, and returns it as stored.
func createLocalTask(t *testing.T, f *guardFixture, title string) *model.Node {
	t.Helper()
	ctx := context.Background()
	seq, err := f.store.NextSequence(ctx, "PROJ:")
	require.NoError(t, err)
	id := model.BuildID("PROJ", "", seq)
	now := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	require.NoError(t, f.store.CreateNode(ctx, &model.Node{
		ID: id, Project: "PROJ", Depth: 0, Seq: seq, Title: title, Description: "Why: " + title,
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-" + title, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, f.store.SetAnnotations(ctx, id, []model.Annotation{{
		ID: "01J9-" + strings.ReplaceAll(title, " ", "-"), Author: "local", Text: "evidence: " + title, CreatedAt: now,
	}}))
	node, err := f.store.GetNode(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, node.UID)
	return node
}

// pulledBoard decodes the .mtix/tasks.json on disk, as mtix import reads it.
func (f *guardFixture) pulledBoard(t *testing.T) *sqlite.ExportData {
	t.Helper()
	data, err := sqlite.DecodeExportData(bytes.NewReader(f.read(t, "tasks.json")))
	require.NoError(t, err)
	return data
}

// activityOf returns the activity entries of node id.
func activityOf(t *testing.T, f *guardFixture, id string) []model.ActivityEntry {
	t.Helper()
	entries, err := f.store.GetActivity(context.Background(), id, 100, 0)
	require.NoError(t, err)
	return entries
}

// assertKeptUnder checks that node id holds the task want, intact: its
// uid, title, description, annotations and activity.
func assertKeptUnder(t *testing.T, f *guardFixture, id string, want *model.Node, wantActivity []model.ActivityEntry) {
	t.Helper()
	got, err := f.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, want.UID, got.UID)
	assert.Equal(t, want.Title, got.Title)
	assert.Equal(t, want.Description, got.Description)
	assert.Equal(t, want.Annotations, got.Annotations)
	assert.Equal(t, wantActivity, activityOf(t, f, id))
}

// TestAutoImport_TwoClonesCreateSameID_RefusedThenMergeRenumbersLocal is the
// two-clone collision: clones A and B of one board each create PROJ-3, and
// A pulls B's board. The auto-import refuses, naming a different task under
// PROJ-3. A merge without confirmation writes nothing; with it, B's task
// takes PROJ-3 and A's moves to PROJ-4 with its uid, title, description,
// annotations and activity. A's export then imports cleanly into B.
func TestAutoImport_TwoClonesCreateSameID_RefusedThenMergeRenumbersLocal(t *testing.T) {
	ctx := context.Background()
	origin := newGuardFixture(t)
	a, b := teammateClone(t, origin), teammateClone(t, origin)
	mine := createLocalTask(t, a, "A's own task")
	theirs := createLocalTask(t, b, "B's own task")
	require.Equal(t, "PROJ-3", mine.ID)
	require.Equal(t, "PROJ-3", theirs.ID)
	require.NotEqual(t, mine.UID, theirs.UID)
	mineActivity := activityOf(t, a, "PROJ-3")
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))

	shareBoard(t, b, a)
	before := a.storeSnapshot(t)
	err := a.svc.AutoImport(ctx, a.mtixDir)
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assert.Equal(t, before, a.storeSnapshot(t))
	msg := a.notices.String()
	assert.Contains(t, msg, `PROJ-3: a different task under this id (local "A's own task", file "B's own task")`)
	assert.Contains(t, msg, "renumbered to the next number free")
	assert.Contains(t, msg, "--confirm")

	report, _, err := a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: mine.UID, OldPath: "PROJ-3", NewPath: "PROJ-4"}}, report.LocalRenumbers)
	assert.Equal(t, before, a.storeSnapshot(t), "merge without --confirm writes nothing")

	_, _, err = a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	got, err := a.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, theirs.UID, got.UID, "the published board keeps the number")
	assertKeptUnder(t, a, "PROJ-4", mine, mineActivity)

	// What mtix import does next: resolve the refusal, then export.
	require.NoError(t, a.svc.ResolveRefusalByImport(a.mtixDir, filepath.Join(a.mtixDir, "tasks.json")))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	shareBoard(t, a, b)
	require.NoError(t, b.svc.AutoImport(ctx, b.mtixDir), "A's export imports cleanly into B")
	assert.Contains(t, b.notices.String(), "mtix: imported the changed .mtix/tasks.json: nodes 1 added, 0 updated, 0 removed")
	onB, err := b.store.GetNode(ctx, "PROJ-4")
	require.NoError(t, err)
	assert.Equal(t, mine.UID, onB.UID)
	onB, err = b.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, theirs.UID, onB.UID)
}

// TestAutoImport_TaskCreatedWhileRefusalPending_RenumberedByMerge verifies a
// task created while a refusal is pending, which takes the id a task on the
// pulled board holds, is named as a different task under that id and is
// renumbered by the merge, not lost.
func TestAutoImport_TaskCreatedWhileRefusalPending_RenumberedByMerge(t *testing.T) {
	ctx := context.Background()
	origin := newGuardFixture(t)
	a, b := teammateClone(t, origin), teammateClone(t, origin)
	theirs := createLocalTask(t, b, "Teammate's task")
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	evidence := model.Annotation{ID: "01J9PENDING000000000000001", Author: "a", Text: "local evidence",
		CreatedAt: time.Date(2026, 9, 24, 10, 30, 0, 0, time.UTC)}
	require.NoError(t, a.store.SetAnnotations(ctx, "PROJ-2", []model.Annotation{evidence}))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	shareBoard(t, b, a)
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused, "b's board lacks the annotation")

	mine := createLocalTask(t, a, "Created while the refusal is pending")
	require.Equal(t, "PROJ-3", mine.ID, "the local counter reuses the pulled board's id")
	mineActivity := activityOf(t, a, "PROJ-3")
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	err := a.svc.AutoImport(ctx, a.mtixDir) // the next command
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assert.Contains(t, a.notices.String(),
		`PROJ-3: a different task under this id (local "Created while the refusal is pending", file "Teammate's task")`)

	_, _, err = a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assertKeptUnder(t, a, "PROJ-4", mine, mineActivity)
	got, err := a.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, theirs.UID, got.UID)
	annotated, err := a.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, []model.Annotation{evidence}, annotated.Annotations, "the merge keeps the local annotation")
}
