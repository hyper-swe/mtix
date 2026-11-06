// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Real-binary, real-Postgres proof of the lost-local-value notice and of the
// printed guidance (MTIX-112.17.16.18). The printed commands run through sh with
// the built binary; the enrolment itself is the shipped service over the shipped
// ports. Gated on MTIX_CMD_PG_TEST_DSN through requireCmdPG.

// realMtix builds the real binary once per test and returns its path.
func realMtix(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mtix")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, "build the real binary: %s", out)
	return binary
}

// subprocessEnv isolates operator state under the home directory (not /tmp, not
// a work tree) and points the binary at the fixture hub.
func subprocessEnv(t *testing.T, binary string) []string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	state, err := os.MkdirTemp(home, ".mtix-enrol-bin-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(state)) })
	return append(os.Environ(), "XDG_CONFIG_HOME="+state, "APPDATA="+state, "MTIX_AUTHOR_ID=lane-a",
		transport.EnvDSN+"="+requireCmdPGDSNOnly(t), "MTIX_BIN="+binary)
}

// runShellWithMtix runs one printed line through sh in the worktree, with mtix
// bound to the real binary.
func runShellWithMtix(t *testing.T, f *enrolPGFixture, line string) (string, error) {
	t.Helper()
	binary := realMtix(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "config.yaml"),
		[]byte("prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"), 0o600))
	cmd := exec.CommandContext(t.Context(), "sh", "-c", `mtix() { "$MTIX_BIN" "$@"; }; `+line) //nolint:gosec // G204: the printed line under test
	cmd.Dir = f.root
	cmd.Env = subprocessEnv(t, binary)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// teammateRetitles has a teammate retitle TEST-1 once per title on the real hub.
func (f *enrolPGFixture) teammateRetitles(t *testing.T, titles ...string) {
	t.Helper()
	ctx := context.Background()
	peer, err := sqlite.New(filepath.Join(t.TempDir(), "peer"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })
	_, _, err = pullLoop(ctx, testIngest(nil), f.pool, peer, transport.PullCursor{}, 100)
	require.NoError(t, err)
	for _, title := range titles {
		require.NoError(t, peer.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	}
	var pushErr bytes.Buffer
	_, err = pushLoop(ctx, &pushErr, f.pool, peer)
	require.NoError(t, err, pushErr.String())
}

// unjournaledTitle gives the fixture's store a title no event gave it (what an
// import merge writes) and refreshes tasks.json to match.
func (f *enrolPGFixture) unjournaledTitle(t *testing.T, title string) {
	t.Helper()
	st := f.open(t)
	_, err := st.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET title = ? WHERE id = 'TEST-1'`, title)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardFromWorktree(t)
}

// TestEnrolWorktree_RealBinary_ThreeTeammateEditsAfterReEnter_NoticeNotSilence is
// the founder-approved design on a real binary and a real hub: the printed
// steps are run exactly (backup, update, sync push), the teammate edited the
// same title three times, and the enrolment goes on with a notice that names the
// node and field, a report, and a doctor record; the old store keeps the value.
func TestEnrolWorktree_RealBinary_ThreeTeammateEditsAfterReEnter_NoticeNotSilence(t *testing.T) {
	ctx := context.Background()
	var notice bytes.Buffer
	f := newEnrolPGFixture(t)
	f.notice = &notice
	f.unjournaledTitle(t, "branch title")
	f.teammateRetitles(t, "teammate 1", "teammate 2", "teammate 3")

	_, err := f.enrol()
	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	runPrintedBackup(t, f, err.Error())
	out, runErr := runShellWithMtix(t, f, "mtix update TEST-1 --title 'branch title'")
	require.NoError(t, runErr, out)
	out, runErr = runShellWithMtix(t, f, "mtix sync push --insecure-tls")
	require.NoError(t, runErr, out)
	f.writeBoardFromWorktree(t)

	res, err := f.enrol()

	require.NoError(t, err, "the lost value is a notice, not a refusal")
	require.NotNil(t, res.LostLocal, "the enrolment must say the value did not survive")
	assert.Equal(t, service.BoardDiff{"TEST-1": {"title"}}, res.LostLocal.Diff)
	assert.Contains(t, notice.String(), "TEST-1 (title)")
	assert.Contains(t, notice.String(), res.AsideDir)
	assert.FileExists(t, res.LostLocalReport)
	clone, getErr := f.open(t).GetNode(ctx, "TEST-1")
	require.NoError(t, getErr)
	assert.Equal(t, "teammate 3", clone.Title, "the hub's value wins at head")
	aside, openErr := sqlite.New(res.AsideDir, slog.Default())
	require.NoError(t, openErr)
	t.Cleanup(func() { _ = aside.Close() })
	kept, getErr := aside.GetNode(ctx, "TEST-1")
	require.NoError(t, getErr)
	assert.Equal(t, "branch title", kept.Title, "the aside store keeps the value")
	rec, recErr := service.ReadHubAccepted(filepath.Join(f.root, ".mtix"))
	require.NoError(t, recErr)
	require.NotNil(t, rec, "doctor warns until the record is cleared")
	assert.Equal(t, res.LostLocalReport, rec.LostLocalReport)
}

// TestEnrolWorktree_RealBinary_ReEnteredValueThatWins_NoNotice: when the
// teammate made one edit the re-entered write wins the order and nothing is lost.
func TestEnrolWorktree_RealBinary_ReEnteredValueThatWins_NoNotice(t *testing.T) {
	var notice bytes.Buffer
	f := newEnrolPGFixture(t)
	f.notice = &notice
	f.unjournaledTitle(t, "branch title")
	f.teammateRetitles(t, "teammate 1")
	_, err := f.enrol()
	require.Error(t, err)
	out, runErr := runShellWithMtix(t, f, "mtix update TEST-1 --title 'branch title' && mtix sync push --insecure-tls")
	require.NoError(t, runErr, out)
	f.writeBoardFromWorktree(t)

	res, err := f.enrol()

	require.NoError(t, err)
	if res.LostLocal == nil {
		assert.Empty(t, notice.String())
		n, getErr := f.open(t).GetNode(context.Background(), "TEST-1")
		require.NoError(t, getErr)
		assert.Equal(t, "branch title", n.Title)
		return
	}
	// A tie on the clock may go to the teammate; then it must be said.
	assert.Contains(t, notice.String(), "TEST-1 (title)")
}

// TestEnrolWorktree_RealHub_RepeatedRefusals_SameDigest: the digest of a
// refusal does not change between runs on an unchanged board.
func TestEnrolWorktree_RealHub_RepeatedRefusals_SameDigest(t *testing.T) {
	f := newEnrolPGFixture(t)
	f.unjournaledTitle(t, "branch title")
	f.teammateRetitles(t, "teammate 1")
	digests := map[string]bool{}
	for i := 0; i < 5; i++ {
		_, err := f.enrol()
		var refusal *service.WorktreeBoardError
		require.ErrorAs(t, err, &refusal)
		digests[refusal.Digest()] = true
	}
	assert.Len(t, digests, 1)
}

// TestEnrolWorktree_RealBinary_HostileWorktreeName_PrintedBackupIsLiteral runs
// the printed backup line from a worktree whose directory name holds a command
// substitution, a space and a quote: the command must not run and the copy must
// exist.
func TestEnrolWorktree_RealBinary_HostileWorktreeName_PrintedBackupIsLiteral(t *testing.T) {
	f := newEnrolPGFixtureNamed(t, "a $(touch PWNED) it's b")
	f.unjournaledTitle(t, "branch title")
	f.teammateRetitles(t, "teammate 1")
	_, err := f.enrol()
	require.Error(t, err)

	copyPath := runPrintedBackup(t, f, err.Error())

	assert.FileExists(t, copyPath)
	assert.NoFileExists(t, filepath.Join(f.root, "PWNED"))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(f.root), "PWNED"))
	assert.False(t, strings.Contains(copyPath, "PWNED") && !strings.Contains(copyPath, "$(touch PWNED)"))
}
