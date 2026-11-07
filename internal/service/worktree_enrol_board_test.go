// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func writeBoard(t *testing.T, w *enrolWorld, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(w.mtix(), "tasks.json"), []byte(body), 0o600))
}

func hubDiffers(ids ...string) *service.BoardFinding {
	diff := service.BoardDiff{}
	for _, id := range ids {
		diff[id] = []string{"status"}
	}
	return &service.BoardFinding{Reason: service.BoardHubDiffers, Diff: diff}
}

func fileDiffers(ids ...string) *service.BoardFinding {
	f := hubDiffers(ids...)
	f.Reason = service.BoardFileDiffers
	return f
}

func reportsIn(t *testing.T, w *enrolWorld) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(w.mtix(), "enroll-diff-*.json"))
	require.NoError(t, err)
	return found
}

// TestEnrolWorktree_BoardDiffers_RefusesBeforeAnythingMoves: the preflight
// reports a finding. The refusal names the reason, the count, the first ids, the
// report and a fix, and the store and the board file are byte-identical after.
func TestEnrolWorktree_BoardDiffers_RefusesBeforeAnythingMoves(t *testing.T) {
	tests := []struct {
		name    string
		finding *service.BoardFinding
		want    []string
	}{
		{"file differs", fileDiffers("TEST-7", "TEST-8"),
			[]string{"tasks.json is not what this worktree's store exports", "2 node(s): TEST-7, TEST-8", "mtix sync --fix", "keep a copy of .mtix/tasks.json"}},
		{"hub differs", hubDiffers("TEST-7", "TEST-8"),
			[]string{"board differs from the hub as this store last applied it", "2 node(s): TEST-7, TEST-8", "accept-hub", "mtix sync push", "mtix sync pull", "aside directory", "no content"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newEnrolWorld(t)
			body := `{"nodes":[{"id":"TEST-7"},{"id":"TEST-8"}]}`
			writeBoard(t, w, body)
			w.boardFinding = tt.finding
			before := treeHash(t, w.data)

			_, err := w.run()

			var refusal *service.WorktreeBoardError
			require.True(t, errors.As(err, &refusal))
			require.ErrorIs(t, err, model.ErrConflict)
			for _, frag := range tt.want {
				assert.Contains(t, err.Error(), frag)
			}
			reports := reportsIn(t, w)
			require.Len(t, reports, 1, "a refusal writes its diff report")
			assert.Equal(t, reports[0], refusal.Report)
			assert.Contains(t, err.Error(), reports[0])
			assert.Equal(t, []string{"snapshot", "preflight"}, w.calls, "no backup, no clone, no enrol")
			assert.Equal(t, before, treeHash(t, w.data))
			got, readErr := os.ReadFile(filepath.Join(w.mtix(), "tasks.json"))
			require.NoError(t, readErr)
			assert.Equal(t, body, string(got))
			w.assertMarker(t, false)
			_, statErr := os.Lstat(filepath.Join(w.mtix(), "backups"))
			assert.ErrorIs(t, statErr, os.ErrNotExist, "no backup was written")
		})
	}
}

// TestEnrolWorktree_BoardDiffers_ReportHoldsIDsAndFieldNamesOnly: the report
// names each node and the fields that differ, and carries no content.
func TestEnrolWorktree_BoardDiffers_ReportHoldsIDsAndFieldNamesOnly(t *testing.T) {
	w := newEnrolWorld(t)
	w.boardFinding = &service.BoardFinding{Reason: service.BoardHubDiffers, Diff: service.BoardDiff{"TEST-7": {"status", "title"}, "TEST-2": {"(node)"}}}

	_, err := w.run()

	var refusal *service.WorktreeBoardError
	require.True(t, errors.As(err, &refusal))
	var report struct {
		Reason    string              `json:"reason"`
		Digest    string              `json:"digest"`
		CreatedAt string              `json:"created_at"`
		Count     int                 `json:"count"`
		Nodes     map[string][]string `json:"nodes"`
	}
	body, readErr := os.ReadFile(refusal.Report)
	require.NoError(t, readErr)
	require.NoError(t, json.Unmarshal(body, &report))
	assert.Equal(t, "hub-differs", report.Reason)
	assert.Equal(t, refusal.Digest(), report.Digest, "the report carries the digest the operator names")
	assert.Equal(t, 2, report.Count)
	assert.Equal(t, "2026-11-03T10:20:30Z", report.CreatedAt)
	assert.Equal(t, map[string][]string{"TEST-7": {"status", "title"}, "TEST-2": {"(node)"}}, report.Nodes)
	assert.Equal(t, filepath.Join(w.mtix(), "enroll-diff-20261103T102030Z.json"), refusal.Report)
}

// TestEnrolWorktree_BoardCannotBeCompared_RefusesBeforeAnythingMoves: a failed
// comparison is never a pass.
func TestEnrolWorktree_BoardCannotBeCompared_RefusesBeforeAnythingMoves(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[]}`)
	w.fail["preflight"] = errors.New("hub unreachable")
	before := treeHash(t, w.data)

	_, err := w.run()

	require.ErrorContains(t, err, "compare the local board with the hub")
	require.ErrorContains(t, err, "hub unreachable")
	assert.Equal(t, []string{"snapshot", "preflight"}, w.calls)
	assert.Equal(t, before, treeHash(t, w.data))
	w.assertMarker(t, false)
	assert.Empty(t, reportsIn(t, w))
}

// TestWorktreeBoardError_ManyIDs_ListsTenAndCountsTheRest pins the cap.
func TestWorktreeBoardError_ManyIDs_ListsTenAndCountsTheRest(t *testing.T) {
	ids := make([]string, 13)
	for i := range ids {
		ids[i] = fmt.Sprintf("TEST-%02d", i)
	}

	msg := (&service.WorktreeBoardError{BoardFinding: *hubDiffers(ids...), Report: "r.json"}).Error()

	assert.Contains(t, msg, "13 node(s)")
	assert.Contains(t, msg, "TEST-09")
	assert.NotContains(t, msg, "TEST-10")
	assert.Contains(t, msg, "and 3 more")
}

// TestEnrolWorktree_ReportCannotBeWritten_IsAnError: a refusal that cannot leave
// its report is an error, and still moves nothing.
func TestEnrolWorktree_ReportCannotBeWritten_IsAnError(t *testing.T) {
	w := newEnrolWorld(t)
	w.boardFinding = hubDiffers("TEST-7")
	require.NoError(t, os.MkdirAll(filepath.Join(w.mtix(), "enroll-diff-20261103T102030Z.json"), 0o700)) // a directory in the way
	before := treeHash(t, w.data)

	_, err := w.run()

	require.ErrorContains(t, err, "write the diff report")
	var refusal *service.WorktreeBoardError
	assert.False(t, errors.As(err, &refusal))
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "backup")
}

// TestEnrolWorktree_ProvenBoard_EnrolsAndMarksItSeen: with the proof holding,
// the hub clone is authoritative whether the file was behind or not, and the
// file's hash is recorded so no command imports it over the clone. The
// post-clone check is given the moved-aside store as the local side.
func TestEnrolWorktree_ProvenBoard_EnrolsAndMarksItSeen(t *testing.T) {
	w := newEnrolWorld(t)
	body := `{"nodes":[{"id":"TEST-1"}]}`
	writeBoard(t, w, body)

	res, err := w.run()

	require.NoError(t, err)
	assert.Contains(t, w.calls, "verify")
	assert.Equal(t, res.AsideDir, w.asideSeen)
	got, readErr := os.ReadFile(filepath.Join(w.data, "sync.sha256"))
	require.NoError(t, readErr)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(body))), string(got))
	assert.Empty(t, res.HubDiffReport)
	assert.Empty(t, reportsIn(t, w))
	_, statErr := os.Lstat(service.HubAcceptedPath(w.mtix()))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestEnrolWorktree_NoBoardFile_StillVerifiesTheClone: a worktree without a
// tasks.json has nothing to record, but the proof still runs.
func TestEnrolWorktree_NoBoardFile_StillVerifiesTheClone(t *testing.T) {
	t.Run("proven: enrols and records nothing", func(t *testing.T) {
		w := newEnrolWorld(t)
		_, err := w.run()
		require.NoError(t, err)
		assert.Contains(t, w.calls, "verify")
		_, statErr := os.Lstat(filepath.Join(w.data, "sync.sha256"))
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})
	t.Run("not proven: restores the original", func(t *testing.T) {
		w := newEnrolWorld(t)
		w.cloneBoardFinding = hubDiffers("TEST-3")
		before := treeHash(t, w.data)

		_, err := w.run()

		var refusal *service.WorktreeBoardError
		require.True(t, errors.As(err, &refusal))
		assert.Equal(t, before, treeHash(t, w.data))
		assert.NotContains(t, w.calls, "enrol")
	})
}

// TestEnrolWorktree_BoardNotProvenOnTheCloneAfterTheSwap_NeverMarkedSeen: the
// preflight passed but the clone itself fails the proof (the hub changed in
// between): the board is not marked seen and the original store goes back.
func TestEnrolWorktree_BoardNotProvenOnTheCloneAfterTheSwap_NeverMarkedSeen(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[{"id":"TEST-7"}]}`)
	w.cloneBoardFinding = hubDiffers("TEST-7")
	before := treeHash(t, w.data)

	_, err := w.run()

	var refusal *service.WorktreeBoardError
	require.True(t, errors.As(err, &refusal))
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Contains(t, err.Error(), "original store was restored")
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "enrol")
	_, statErr := os.Lstat(filepath.Join(w.data, "sync.sha256"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a board that fails the proof is never marked seen")
	w.assertMarker(t, false)
}

// TestEnrolWorktree_VerifyFails_NeverMarkedSeen: a comparison error after the
// swap is never a pass either.
func TestEnrolWorktree_VerifyFails_NeverMarkedSeen(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[]}`)
	w.fail["verify"] = errors.New("clone unreadable")
	before := treeHash(t, w.data)

	_, err := w.run()

	require.ErrorContains(t, err, "clone unreadable")
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "enrol")
}

func TestEnrolWorktree_BoardChangesWhileChecked_RestoresTheOriginalWithASentinel(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[]}`)
	orig := w.opts.Ports.VerifyBoard
	w.opts.Ports.VerifyBoard = func(c context.Context, r, aside string) (*service.BoardFinding, error) {
		require.NoError(t, os.WriteFile(filepath.Join(w.mtix(), "tasks.json"), []byte(`{"nodes":[{"id":"TEST-9"}]}`), 0o600))
		return orig(c, r, aside)
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)
	before := treeHash(t, w.data)

	_, err := w.run()

	require.ErrorContains(t, err, "changed while enrolment checked it")
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, before, treeHash(t, w.data))
}

func (w *enrolWorld) runAccepting(digest string) (*service.WorktreeEnrolResult, error) {
	return w.svc.EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: w.root, Mode: "yes", Source: "test", AcceptHub: digest})
}

// TestEnrolWorktree_AcceptHub_ProceedsKeepsTheOldStoreAndRecordsTheReport: the
// operator who read the report names accept-hub: the enrolment proceeds on the
// hub's version, the old store and the backup stay, the report path is recorded
// for doctor, and nothing is dropped silently.
func TestEnrolWorktree_AcceptHub_ProceedsKeepsTheOldStoreAndRecordsTheReport(t *testing.T) {
	w := newEnrolWorld(t)
	writeBoard(t, w, `{"nodes":[{"id":"TEST-7"}]}`)
	w.boardFinding = hubDiffers("TEST-7")
	w.cloneBoardFinding = hubDiffers("TEST-7")

	res, err := w.runAccepting(hubDiffers("TEST-7").Digest())

	require.NoError(t, err)
	assert.Contains(t, w.calls, "enrol")
	assert.DirExists(t, res.AsideDir, "the old store is kept")
	assert.FileExists(t, res.BackupPath, "the backup is kept")
	require.NotEmpty(t, res.HubDiffReport)
	assert.FileExists(t, res.HubDiffReport)
	rec, readErr := service.ReadHubAccepted(w.mtix())
	require.NoError(t, readErr)
	require.NotNil(t, rec)
	assert.Equal(t, res.HubDiffReport, rec.Report)
	assert.Equal(t, res.AsideDir, rec.AsideDir)
	assert.Equal(t, res.BackupPath, rec.BackupPath)
	assert.Equal(t, "2026-11-03T10:20:30Z", rec.AcceptedAt)
	w.assertMarker(t, false)
}

// TestEnrolWorktree_AcceptHub_NeverCoversTheBoardFileRule: accept-hub is for the
// operator who read a hub diff; a board file that is not the store's export has
// its own fix and is still refused.
func TestEnrolWorktree_AcceptHub_NeverCoversTheBoardFileRule(t *testing.T) {
	w := newEnrolWorld(t)
	w.boardFinding = fileDiffers("TEST-7")
	before := treeHash(t, w.data)

	_, err := w.runAccepting(fileDiffers("TEST-7").Digest())

	var refusal *service.WorktreeBoardError
	require.True(t, errors.As(err, &refusal))
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "backup")
}

// TestEnrolWorktree_AcceptHub_ComparisonErrorIsStillAnError: accepting a
// difference never accepts a failed comparison.
func TestEnrolWorktree_AcceptHub_ComparisonErrorIsStillAnError(t *testing.T) {
	w := newEnrolWorld(t)
	w.fail["preflight"] = errors.New("hub unreachable")

	_, err := w.runAccepting("anything")

	require.ErrorContains(t, err, "hub unreachable")
	assert.NotContains(t, w.calls, "backup")
}

// TestEnrolWorktree_AcceptHub_RolledBackEnrolmentLeavesNoAcceptRecord: when the
// enrolment fails after the record was written, the original store goes back and
// the record goes with it, so doctor never warns about an enrolment that did not
// happen.
func TestEnrolWorktree_AcceptHub_RolledBackEnrolmentLeavesNoAcceptRecord(t *testing.T) {
	w := newEnrolWorld(t)
	w.boardFinding = hubDiffers("TEST-7")
	w.fail["enrol"] = errors.New("host refused")

	_, err := w.runAccepting(hubDiffers("TEST-7").Digest())

	require.ErrorContains(t, err, "host refused")
	rec, readErr := service.ReadHubAccepted(w.mtix())
	require.NoError(t, readErr)
	assert.Nil(t, rec)
}

func TestReadHubAccepted_NoneOrTorn(t *testing.T) {
	dir := t.TempDir()
	rec, err := service.ReadHubAccepted(dir)
	require.NoError(t, err)
	assert.Nil(t, rec)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "enroll.d"), 0o700))
	require.NoError(t, os.WriteFile(service.HubAcceptedPath(dir), []byte("{torn"), 0o600))
	_, err = service.ReadHubAccepted(dir)
	require.ErrorContains(t, err, "decode the accept-hub record")
}

// TestEnrolWorktree_AcceptHub_DigestOfAnotherReport_RefusedNothingMoved: the
// operator read a report for TEST-7; the difference is now TEST-7 and TEST-8.
// The digest no longer matches, so nothing is accepted and the refusal says why.
func TestEnrolWorktree_AcceptHub_DigestOfAnotherReport_RefusedNothingMoved(t *testing.T) {
	w := newEnrolWorld(t)
	w.boardFinding = hubDiffers("TEST-7", "TEST-8")
	before := treeHash(t, w.data)

	_, err := w.runAccepting(hubDiffers("TEST-7").Digest())

	var refusal *service.WorktreeBoardError
	require.True(t, errors.As(err, &refusal))
	assert.True(t, refusal.StaleAccept)
	assert.Contains(t, err.Error(), "changed after the report you read")
	assert.Contains(t, err.Error(), hubDiffers("TEST-7", "TEST-8").Digest(), "the new digest is printed")
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "backup")
	assert.NotContains(t, w.calls, "enrol")
}

// TestEnrolWorktree_AcceptHub_DigestOfAnotherReport_AtTheVerify_RollsBack: the
// preflight difference matches the digest but the board changed by the verify;
// the enrolment rolls back to the original store.
func TestEnrolWorktree_AcceptHub_DigestMismatchAtTheVerify_RestoresTheOriginal(t *testing.T) {
	w := newEnrolWorld(t)
	w.boardFinding = hubDiffers("TEST-7")
	w.cloneBoardFinding = hubDiffers("TEST-7", "TEST-9")
	before := treeHash(t, w.data)

	_, err := w.runAccepting(hubDiffers("TEST-7").Digest())

	var refusal *service.WorktreeBoardError
	require.True(t, errors.As(err, &refusal))
	assert.True(t, refusal.StaleAccept)
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "enrol")
}

func TestBoardFinding_Digest_IsStableAndNamesTheWholeDifference(t *testing.T) {
	a := hubDiffers("TEST-7", "TEST-8")
	b := hubDiffers("TEST-8", "TEST-7")
	assert.Equal(t, a.Digest(), b.Digest(), "map order is not part of it")
	assert.NotEqual(t, a.Digest(), hubDiffers("TEST-7").Digest(), "an added id changes it")
	c := hubDiffers("TEST-7", "TEST-8")
	c.Diff["TEST-7"] = []string{"status", "title"}
	assert.NotEqual(t, a.Digest(), c.Digest(), "an added field changes it")
	d := hubDiffers("TEST-7", "TEST-8")
	d.Reason = service.BoardFileDiffers
	assert.NotEqual(t, a.Digest(), d.Digest(), "the reason changes it")
	assert.NotEqual(t, hubDiffers("TEST-7").Digest(), hubDiffers("TEST-8").Digest(), "another id with the same field changes it")
	assert.Len(t, a.Digest(), 16)
}

// TestWorktreeBoardError_HubDifference_NoShapeKnown_TreatedAsLocal: a finding
// that does not say the store is merely missing hub rows gets the safe
// wording: keep a copy, re-enter, accept; never a pull as the fix.
func TestWorktreeBoardError_HubDifference_NoShapeKnown_TreatedAsLocal(t *testing.T) {
	f := hubDiffers("TEST-7")
	err := &service.WorktreeBoardError{BoardFinding: *f, Report: "/r.json"}

	msg := err.Error()

	assert.NotContains(t, msg, "fix: run mtix sync pull")
	assert.Contains(t, msg, "accept-hub "+f.Digest())
	assert.Contains(t, msg, "(1) keep a copy")
}
