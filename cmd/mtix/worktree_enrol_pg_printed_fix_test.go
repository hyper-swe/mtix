// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// importMergedTitle gives the fixture's store an unjournaled title on TEST-1
// (what an import merge writes) and has a teammate retitle the same node on the
// real hub: the F17 state.
func (f *enrolPGFixture) importMergedTitle(t *testing.T, branchTitle, teammateTitle string) {
	t.Helper()
	ctx := context.Background()
	st := f.open(t)
	_, err := st.WriteDB().ExecContext(ctx, `UPDATE nodes SET title = ? WHERE id = 'TEST-1'`, branchTitle)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)
	peer, err := sqlite.New(filepath.Join(t.TempDir(), "peer"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	_, _, err = pullLoop(ctx, testIngest(nil), f.pool, peer, transport.PullCursor{}, 100)
	require.NoError(t, err)
	require.NoError(t, peer.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &teammateTitle}))
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, peer)
	require.NoError(t, err, pushErr.String())
}

var printedBackup = regexp.MustCompile(`(?m)^  \(1\) keep a copy: (mtix backup .*)$`)

// runPrintedBackup runs the printed `mtix backup <path>` line itself, through
// sh, with the real binary standing in for mtix, in the worktree. It returns the
// copy it made.
func runPrintedBackup(t *testing.T, f *enrolPGFixture, message string) string {
	t.Helper()
	m := printedBackup.FindStringSubmatch(message)
	require.Len(t, m, 2, "the refusal prints a backup command with a path: %s", message)
	out, err := runShellWithMtix(t, f, m[1])
	require.NoError(t, err, out)
	copies, globErr := filepath.Glob(filepath.Join(f.root, ".mtix", "enrol-copy-*.db"))
	require.NoError(t, globErr)
	require.Len(t, copies, 1, "the printed command made one copy")
	return copies[0]
}

// TestEnrolWorktree_RealHub_PrintedFixesForALocalChange_KeepTheValue_PG proves
// ruling r5 point 1 on a real hub: the F17 state refuses without printing a
// pull; the printed backup is run and holds the branch title; re-entering the
// change as a normal write, pushing and enrolling again enrols with the branch
// title on the clone and on the hub.
func TestEnrolWorktree_RealHub_PrintedFixesForALocalChange_KeepTheValue_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	f.importMergedTitle(t, "branch title", "teammate title")

	_, err := f.enrol()

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.NotContains(t, err.Error(), "fix: run mtix sync pull")
	copyPath := runPrintedBackup(t, f, err.Error())
	assert.FileExists(t, copyPath)
	body, err := os.ReadFile(copyPath) //nolint:gosec // G304: the backup the test just made
	require.NoError(t, err)
	assert.Contains(t, string(body), "branch title", "the copy holds the value the refusal protected")

	st := f.open(t)
	branch := "branch title"
	require.NoError(t, st.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &branch}))
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, st)
	require.NoError(t, err, pushErr.String())
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)

	_, err = f.enrol()

	require.NoError(t, err)
	clone := f.open(t)
	n, err := clone.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "branch title", n.Title, "the value survived the enrolment")
}

// TestEnrolWorktree_RealHub_AcceptHubKeepsTheLocalValueInTheAsideStore_PG: the
// third printed step takes the hub's version and keeps the old store, which
// still holds the branch title.
func TestEnrolWorktree_RealHub_AcceptHubKeepsTheLocalValueInTheAsideStore_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	f.importMergedTitle(t, "branch title", "teammate title")
	_, err := f.enrol()
	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)

	res, err := f.service().EnrolWorktree(ctx, service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test", AcceptHub: refusal.Digest()})

	require.NoError(t, err)
	aside, err := sqlite.New(res.AsideDir, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = aside.Close() })
	n, err := aside.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "branch title", n.Title)
	clone := f.open(t)
	got, err := clone.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "teammate title", got.Title)
}

// TestEnrolWorktree_RealHub_AutoImportedPushedBoardWithAComment_Enrols_PG is
// ruling r5 point 8 on a real hub: a store that never pulled and holds no
// journal row (what the first-session auto-import leaves) with a pushed board
// and a comment enrols with no steps.
func TestEnrolWorktree_RealHub_AutoImportedPushedBoardWithAComment_Enrols_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	st := f.open(t)
	_, err := st.WriteDB().ExecContext(ctx,
		`DELETE FROM sync_events; UPDATE meta SET value = '0' WHERE key = 'meta.sync.last_pulled_clock'; UPDATE meta SET value = '' WHERE key = 'meta.sync.last_pulled_event_id'`)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)

	_, err = f.enrol()

	require.NoError(t, err)
	require.Len(t, f.enrolled, 1)
}
