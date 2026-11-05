// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
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

func hubLacks(ids ...string) *service.BoardFinding {
	return &service.BoardFinding{Reason: service.BoardHubLacks, IDs: ids}
}

// TestEnrolWorktree_BoardNotProvenOnTheHub_RefusesBeforeAnythingMoves: the
// preflight reports a finding. The refusal names the reason, the count and a
// fix, and the store, the board file and the marker directory are
// byte-identical after.
func TestEnrolWorktree_BoardNotProvenOnTheHub_RefusesBeforeAnythingMoves(t *testing.T) {
	tests := []struct {
		name    string
		finding *service.BoardFinding
		want    []string
	}{
		{"file differs", &service.BoardFinding{Reason: service.BoardFileDiffers, IDs: []string{"TEST-7", "TEST-8"}},
			[]string{"tasks.json is not what this worktree's store exports", "2 node(s): TEST-7, TEST-8", "mtix sync --fix"}},
		{"hub lacks", hubLacks("TEST-7", "TEST-8"),
			[]string{"holds work the hub does not hold", "2 node(s): TEST-7, TEST-8", "mtix sync reconcile", "mtix sync pull"}},
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
}

// TestWorktreeBoardError_ManyIDs_ListsTenAndCountsTheRest pins the cap.
func TestWorktreeBoardError_ManyIDs_ListsTenAndCountsTheRest(t *testing.T) {
	ids := make([]string, 13)
	for i := range ids {
		ids[i] = fmt.Sprintf("TEST-%02d", i)
	}

	msg := (&service.WorktreeBoardError{BoardFinding: *hubLacks(ids...)}).Error()

	assert.Contains(t, msg, "13 node(s)")
	assert.Contains(t, msg, "TEST-09")
	assert.NotContains(t, msg, "TEST-10")
	assert.Contains(t, msg, "and 3 more")
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
}

// TestEnrolWorktree_NoBoardFile_StillVerifiesTheClone: a worktree without a
// tasks.json has nothing to record, but the proof about the nodes still runs.
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
		w.cloneBoardFinding = hubLacks("TEST-3")
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
	w.cloneBoardFinding = hubLacks("TEST-7")
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
