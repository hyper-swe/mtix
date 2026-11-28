// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestRenameNoReplace_Cases(t *testing.T) {
	for _, tt := range []struct {
		name string
		move func(oldPath, newPath string) error
	}{
		{"platform primitive", renameNoReplace},
		{"portable fallback", renameThenRecheck},
	} {
		t.Run(tt.name+"/target absent moves a directory", func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "lock"), filepath.Join(dir, "lock.aside")
			require.NoError(t, os.Mkdir(src, 0o700))
			require.NoError(t, tt.move(src, dst))
			_, err := os.Lstat(dst)
			assert.NoError(t, err)
			_, err = os.Lstat(src)
			assert.True(t, os.IsNotExist(err))
		})
		t.Run(tt.name+"/target present is never replaced", func(t *testing.T) {
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "lock"), filepath.Join(dir, "lock.aside")
			require.NoError(t, os.Mkdir(src, 0o700))
			require.NoError(t, os.WriteFile(dst, []byte("keep"), 0o600))
			err := tt.move(src, dst)
			require.ErrorIs(t, err, model.ErrAlreadyExists)
			body, readErr := os.ReadFile(dst)
			require.NoError(t, readErr)
			assert.Equal(t, "keep", string(body))
			_, err = os.Lstat(src)
			assert.NoError(t, err, "the source stays where it was")
		})
	}
}

// A regular file that appears at the lock path (a live enrolment took the lock
// after the type check) is a held lock: the move must put it back.
func TestMoveEnrolLockAside_RegularFileMoved_IsPutBack(t *testing.T) {
	dir := t.TempDir()
	lock, aside := filepath.Join(dir, "enroll.lock"), filepath.Join(dir, "enroll.lock.unprobeable")
	require.NoError(t, os.WriteFile(lock, []byte("held"), 0o600))
	err := moveEnrolLockAside(lock, aside)
	require.ErrorIs(t, err, model.ErrConflict)
	body, readErr := os.ReadFile(lock)
	require.NoError(t, readErr, "a regular lock must be back in place")
	assert.Equal(t, "held", string(body))
	_, statErr := os.Lstat(aside)
	assert.True(t, os.IsNotExist(statErr))
}

// A failing post-move check cannot show what moved, so the move must fail
// rather than report success.
func TestMoveEnrolLockAside_PostMoveLstatFails_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	lock, aside := filepath.Join(dir, "enroll.lock"), filepath.Join(dir, "enroll.lock.unprobeable")
	require.NoError(t, os.Mkdir(lock, 0o700))
	injected := errors.New("injected lstat failure")
	failingLstat := func(string) (os.FileInfo, error) { return nil, injected }

	err := moveEnrolLockAsideWith(lock, aside, failingLstat)
	require.ErrorIs(t, err, injected)
	assert.Contains(t, err.Error(), "verify moved enrolment lock")
}

// A non-regular object moved aside is a success: the move returns nil.
func TestMoveEnrolLockAside_DirectoryMoved_Succeeds(t *testing.T) {
	dir := t.TempDir()
	lock, aside := filepath.Join(dir, "enroll.lock"), filepath.Join(dir, "enroll.lock.unprobeable")
	require.NoError(t, os.Mkdir(lock, 0o700))
	require.NoError(t, moveEnrolLockAside(lock, aside))
	info, err := os.Lstat(aside)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// Pins the accepted move-then-recheck order (MTIX-112.17.16.21.2.2, founder
// decision DESK-1.248): if a third process creates a fresh lock at the lock
// path inside the window, the held lock cannot be put back. The move fails
// closed with ErrConflict and ErrAlreadyExists, the held lock stays under the
// aside name with its content, and the fresh lock is never replaced.
func TestMoveEnrolLockAside_ThirdActorLockInWindow_HeldLockStaysAside(t *testing.T) {
	dir := t.TempDir()
	lock, aside := filepath.Join(dir, "enroll.lock"), filepath.Join(dir, "enroll.lock.unprobeable")
	require.NoError(t, os.WriteFile(lock, []byte("held"), 0o600))
	thirdActor := func(path string) (os.FileInfo, error) {
		require.NoError(t, os.WriteFile(lock, []byte("fresh"), 0o600))
		return os.Lstat(path)
	}

	err := moveEnrolLockAsideWith(lock, aside, thirdActor)
	require.ErrorIs(t, err, model.ErrConflict)
	require.ErrorIs(t, err, model.ErrAlreadyExists)
	assert.Contains(t, err.Error(), aside)
	fresh, readErr := os.ReadFile(lock)
	require.NoError(t, readErr)
	assert.Equal(t, "fresh", string(fresh), "the third actor's lock is never replaced")
	held, readErr := os.ReadFile(aside)
	require.NoError(t, readErr)
	assert.Equal(t, "held", string(held), "the held lock stays under the aside name")
}

// The nine operator-facing copies describing enrolment lock handling carry the
// one sentence that states the accepted window.
func TestEnrolLockWindowSentence_PresentInEveryCopy(t *testing.T) {
	const sentence = "The move is not atomic with its check: a lock created in a microsecond window can sit under the aside name until it is put back, and a third process taking a fresh lock then can briefly hold alongside it; this is accepted, so run the fix with no other mtix process in the worktree."
	for _, rel := range []string{
		"internal/docs/templates/troubleshooting.md.tmpl", "docs/TROUBLESHOOTING.md", "USERMANUAL.md",
		"internal/docs/templates/skills/admin.md.tmpl", "internal/docs/templates/skills/multi_agent.md.tmpl",
		".claude-plugin/skills/mtix-admin.md", ".claude-plugin/skills/mtix-multi-agent.md",
		".codex-plugin/skills/admin/SKILL.md", ".codex-plugin/skills/multi-agent/SKILL.md",
	} {
		body, err := os.ReadFile(filepath.Join("..", "..", rel))
		require.NoError(t, err, rel)
		flat := strings.Join(strings.Fields(string(body)), " ")
		assert.Contains(t, flat, sentence, rel)
	}
}
