// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hubOf serves events as a hub does: the cursor pass, the id listing of the
// late-event sweep and the lookup by id.
func hubOf(events []*model.SyncEvent) pulledHub {
	return pulledHub{&fakeLateHub{pullEvents: events, events: events, hubNows: []time.Time{offlineEventsPushedAt.Add(24 * time.Hour)}}}
}

// worktreeWith builds a hub-board worktree directory whose store is filled by
// build, with every event it wrote settled on the hub (returned as the hub's
// events), the hub-connected mark set and tasks.json written from the store.
func worktreeWith(t *testing.T, build func(ctx context.Context, st *sqlite.Store)) (root string, writer *sqlite.Store, hubEvents []*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	root, writer, _ = writerWorktreeOnHub(t)
	_, err := writer.WriteDB().ExecContext(ctx, `DELETE FROM sync_events; DELETE FROM nodes; DELETE FROM task_history`)
	require.NoError(t, err)
	build(ctx, writer)
	hubEvents, err = readPendingBatch(ctx, writer, 1000)
	require.NoError(t, err)
	for i, e := range hubEvents {
		e.CreatedAt = offlineEventsPushedAt.Add(time.Duration(i) * time.Second)
	}
	_, err = writer.WriteDB().ExecContext(ctx, `UPDATE sync_events SET sync_status = 'applied'`)
	require.NoError(t, err)
	writeBoardFromStore(t, root, writer)
	return root, writer, hubEvents
}

// pushedWork is what agents author locally and push that a clone reproduces up
// to the normalized set: nested tasks, comments, a claim, completion, a
// dependency, an edit and a cancel.
func pushedWork(ctx context.Context, t *testing.T, st *sqlite.Store) {
	t.Helper()
	now := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "parent")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1.1", "TEST-1", 1, 1, "child")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "commented")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-4", "", 0, 4, "claimed and done")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-7", "", 0, 7, "cancelled")))
	require.NoError(t, st.SetAnnotations(ctx, "TEST-2", []model.Annotation{{ID: "local-comment", Author: "lane", Text: "a comment", CreatedAt: now}}))
	require.NoError(t, st.ClaimNode(ctx, "TEST-4", "lane-a"))
	require.NoError(t, st.TransitionStatus(ctx, "TEST-4", model.StatusInProgress, "start", "lane-a"))
	require.NoError(t, st.TransitionStatus(ctx, "TEST-4", model.StatusDone, "finished", "lane-a"))
	require.NoError(t, st.AddDependency(ctx, &model.Dependency{FromID: "TEST-2", ToID: "TEST-1", DepType: model.DepTypeBlocks, CreatedAt: now}))
	desc := "edited"
	require.NoError(t, st.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Description: &desc}))
	require.NoError(t, st.CancelNode(ctx, "TEST-7", "no longer needed", "lane", false))
}

// notReplicated is work whose effect a clone of the hub does not reproduce
// today: a defer wake time, a cascade delete, an undelete. Each stays a real
// difference.
func notReplicated(ctx context.Context, t *testing.T, st *sqlite.Store) {
	t.Helper()
	now := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-3", "", 0, 3, "deferred")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-5", "", 0, 5, "cascade deleted")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-5.1", "TEST-5", 1, 1, "cascade child")))
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-6", "", 0, 6, "deleted then restored")))
	until := now.Add(48 * time.Hour)
	require.NoError(t, st.DeferNode(ctx, "TEST-3", &until, "later", "lane"))
	require.NoError(t, st.DeleteNode(ctx, "TEST-5", true, "lane"))
	require.NoError(t, st.DeleteNode(ctx, "TEST-6", false, "lane"))
	require.NoError(t, st.UndeleteNode(ctx, "TEST-6"))
}

// TestEnrolWorktree_RealPath_FullyPushedBoard_Enrols: a board every one of whose
// changes is on the hub enrols. The comparison is the whole export of the local
// store pulled to the hub's head against a clone of the hub.
func TestEnrolWorktree_RealPath_FullyPushedBoard_Enrols(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	if errors.As(err, &refusal) {
		t.Logf("DIFF %v", refusal.Diff)
	}
	require.NoError(t, err)
	assert.True(t, *recorded)
	_, statErr := os.Stat(filepath.Join(root, ".mtix", "enroll.d", "hub-accepted.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	reports, _ := filepath.Glob(filepath.Join(root, ".mtix", "enroll-diff-*.json"))
	assert.Empty(t, reports)
}

// TestEnrolWorktree_RealPath_ExclusionsEachHideARealDifference: a pushed board
// with pre-.15 creation/workflow events differs on activity, creation stamps,
// annotations and agent registrations before the exclusions hide those fields.
// Legacy backfill without an addressee now refuses separately.
func TestEnrolWorktree_RealPath_ExclusionsEachHideARealDifference(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	hubEvents = preReplicatedFieldsEvents(t, hubEvents)
	hub := hubOf(hubEvents)
	var local, clone []byte
	require.NoError(t, withScratch(slog.Default(), root, func(scratch string) error {
		return withBoardStore(slog.Default(), root, worktreeDataDir(root), func(st *sqlite.Store) (e error) {
			if local, e = exportBytes(ctx, st); e != nil {
				return e
			}
			clone, e = boundedCloneExport(ctx, slog.Default(), root, scratch, st, hub)
			return e
		})
	}))

	raw, err := service.CompareBoardDocsWith(local, clone, nil)
	require.NoError(t, err)
	for _, field := range []string{"activity", "created_at", "annotations"} {
		found := false
		for _, fields := range raw {
			for _, f := range fields {
				found = found || f == field
			}
		}
		assert.True(t, found, "an unfiltered comparison differs on "+field)
	}
	assert.Contains(t, raw, "agents:lane-a", "the agents section differs on a pushed board")
	filtered, err := service.CompareBoardDocs(local, clone)
	require.NoError(t, err)
	assert.Empty(t, filtered)
}

// Old backfill omitted the addressee; the strict comparison must report it.
func TestEnrolWorktree_RealPath_LegacyBackfilledAddressee_Refuses(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "commented")))
		require.NoError(t, st.AppendAnnotation(ctx, "TEST-2", model.Annotation{ID: "c1", Author: "other", Text: "for you", CreatedAt: now, Addressee: "lane-b"}))
	})
	for _, event := range hubEvents {
		if event.OpType == model.OpComment {
			payload, err := model.EncodePayload(&model.CommentPayload{AuthorID: "other", Body: "for you"})
			require.NoError(t, err)
			event.Payload = payload
			event.WallClockTS = now.UnixMilli()
		}
	}
	require.NoError(t, writer.Close())
	hub := hubOf(hubEvents)
	var local, clone []byte
	require.NoError(t, withScratch(slog.Default(), root, func(scratch string) error {
		return withBoardStore(slog.Default(), root, worktreeDataDir(root), func(st *sqlite.Store) (e error) {
			if local, e = exportBytes(ctx, st); e != nil {
				return e
			}
			clone, e = boundedCloneExport(ctx, slog.Default(), root, scratch, st, hub)
			return e
		})
	}))
	diff, err := service.CompareBoardDocs(local, clone)
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-2": {"annotations"}}, diff)
	svc, recorded := enrolOverHub(fakeDial(hub))
	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))
	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, diff, refusal.Diff)
	assert.False(t, *recorded)
}

// TestEnrolWorktree_RealPath_NotReplicatedWork_RefusesNamesItAndAcceptHubGoes:
// a defer wake time, a cascade delete and an undelete are not on a clone of the
// hub today, so the board refuses and the report names the fields. accept-hub
// then enrols, keeps the old store, and doctor warns.
func TestEnrolWorktree_RealPath_NotReplicatedWork_RefusesNamesItAndAcceptHubGoes(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { notReplicated(ctx, t, st) })
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
	assert.Contains(t, refusal.Diff["TEST-3"], "defer_until")
	assert.Contains(t, refusal.Diff["TEST-6"], "deleted_at")
	report := string(fileBytes(t, refusal.Report))
	assert.Contains(t, report, "defer_until")
	assert.NotContains(t, report, "deferred", "ids and field names only, no content")
	assertNothingChangedExceptReport(t, root, storeBefore, board, recorded)

	req := enrolRequest(root)
	req.AcceptHub = refusal.Digest()
	res, err := svc.EnrolWorktree(ctx, req)
	require.NoError(t, err)
	assert.True(t, *recorded)
	assert.DirExists(t, res.AsideDir)
	assert.FileExists(t, res.BackupPath)
	rec, err := service.ReadHubAccepted(filepath.Join(root, ".mtix"))
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, res.HubDiffReport, rec.Report)

	dsvc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerEnrolHubAcceptedDoctor(dsvc, filepath.Join(root, ".mtix")))
	rows, runErr := dsvc.Run(ctx, service.DoctorRunOptions{})
	require.NoError(t, runErr, "a warning does not fail the run")
	var found bool
	for _, r := range rows {
		if r.ID == "board.enrol-hub-accepted" {
			found = true
			assert.Equal(t, service.DoctorWarn, r.Status)
			assert.Contains(t, r.Fix, "hub-accepted.json")
		}
	}
	assert.True(t, found)
}

func assertNothingChangedExceptReport(t *testing.T, root string, storeBefore map[string]string, board []byte, recorded *bool) {
	t.Helper()
	assert.False(t, *recorded)
	assert.Equal(t, storeBefore, treeBytes(t, worktreeDataDir(root)))
	assert.Equal(t, board, fileBytes(t, boardFilePath(root)))
	_, statErr := os.Lstat(filepath.Join(root, ".mtix", "enroll.d", "in-progress.json"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// preReplicatedFieldsEvents removes snapshots absent from old comment creation
// and workflow events, preserving the other payload fields (including to).
// A caller modelling an old resolve must also supply its bulk-placeholder body
// and empty author_id. Copies preserve the original journal fixtures.
func preReplicatedFieldsEvents(t *testing.T, events []*model.SyncEvent) []*model.SyncEvent {
	t.Helper()
	out := make([]*model.SyncEvent, 0, len(events))
	for _, event := range events {
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		delete(payload, "annotation")
		delete(payload, "closed_at")
		copied := *event
		var err error
		copied.Payload, err = json.Marshal(payload)
		require.NoError(t, err)
		out = append(out, &copied)
	}
	return out
}
