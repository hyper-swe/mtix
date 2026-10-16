// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func verifiedIdentityWorktree(t *testing.T, common, name string) string {
	t.Helper()
	root := t.TempDir()
	admin := filepath.Join(common, "worktrees", name)
	require.NoError(t, os.MkdirAll(admin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+admin+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(root, ".git")+"\n"), 0600))
	return root
}

func TestResolveAgentID_NewWorktree_FallsBackToMainCheckoutLane(t *testing.T) {
	_, s, loc := identityFixture(t)
	common := filepath.Join(loc.Root, ".git")
	require.NoError(t, os.MkdirAll(common, 0700))
	require.NoError(t, s.LearnLane(t.Context(), loc, "main-lane"))
	worktree := verifiedIdentityWorktree(t, common, "new")
	f := newRegFixture(t)
	loc.Root, loc.Path, loc.BoardKey = worktree, worktree, "22222222-2222-4222-8222-222222222222"
	f.svc.Routing = &service.AttachRouting{State: s, Board: service.AttachBoard{Key: loc.BoardKey, Path: worktree}, LanePath: worktree}
	result, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{})
	require.NoError(t, err)
	require.Equal(t, "main-lane", result.ID)
	require.True(t, result.Silent)
	require.Equal(t, "learned-lane", result.Reason)
}

func TestResolveAgentID_OtherBoardKeySameRoot_Refuses(t *testing.T) {
	f, s, loc := identityFixture(t)
	other := loc
	other.BoardKey = "22222222-2222-4222-8222-222222222222"
	require.NoError(t, s.LearnLane(t.Context(), other, "other-board-lane"))
	_, err := f.svc.ResolveAgentID(t.Context(), other, service.IdentityEnv{})
	require.Error(t, err)
}

func TestResolveAgentID_SameHandleOtherHarness_DoesNotResume(t *testing.T) {
	f, s, loc := identityFixture(t)
	request := req("other-harness", 1)
	request.Harness = "pi"
	request.SessionHandle = "shared"
	_, err := f.svc.RegisterSession(t.Context(), request)
	require.NoError(t, err)
	f.live[1] = service.LiveAlive
	require.NoError(t, s.LearnLane(t.Context(), loc, "own-harness"))
	result, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{SessionHandle: "shared"})
	require.NoError(t, err)
	require.Equal(t, "own-harness", result.ID)
	require.True(t, result.Silent)
	require.Equal(t, "learned-lane", result.Reason)
}

func TestResolveAgentID_NilLiveness_DoesNotSilentlyReuse(t *testing.T) {
	f, _, loc := identityFixture(t)
	_, err := f.svc.RegisterSession(t.Context(), req("lane-a", 1))
	require.NoError(t, err)
	f.svc.Liveness = nil
	result, err := f.svc.ResolveAgentID(t.Context(), loc, service.IdentityEnv{})
	require.NoError(t, err)
	require.Empty(t, result.ID)
	require.False(t, result.Silent)
}
