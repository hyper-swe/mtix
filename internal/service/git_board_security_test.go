// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Owned real Git configuration and environment canaries pin read-only process policy.
package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func boardScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "owned-command")
	boardWrite(t, path, []byte("#!/bin/sh\n"+body+"\n"))
	require.NoError(t, os.Chmod(path, 0700))
	return path
}

func boardPositiveControl(t *testing.T, path, marker string) {
	t.Helper()
	ctx, cancel := boardTestWatchdog(t)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	err := cmd.Run()
	require.NoError(t, ctx.Err(), "positive canary watchdog expired")
	require.NoError(t, err)
	require.FileExists(t, marker)
	require.NoError(t, os.Remove(marker))
}

func TestMergeBaseBoard_HostileRepoConfig_ExecutesNothing(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	marker := filepath.Join(t.TempDir(), "canary")
	script := boardScript(t, "printf observed > '"+marker+"'")
	boardPositiveControl(t, script, marker)
	hooks := t.TempDir()
	for _, name := range []string{"pre-commit", "post-checkout", "post-merge"} {
		boardWrite(t, filepath.Join(hooks, name), []byte("#!/bin/sh\nprintf observed > '"+marker+"'\n"))
		require.NoError(t, os.Chmod(filepath.Join(hooks, name), 0700))
	}
	settings := map[string]string{"core.hooksPath": hooks, "core.fsmonitor": script, "core.pager": script, "credential.helper": "!" + script, "diff.external": script, "diff.fixture.textconv": script, "protocol.file.allow": "always"}
	for key, value := range settings {
		boardGit(t, root, "config", key, value)
	}
	boardWrite(t, filepath.Join(root, ".gitattributes"), []byte(".mtix/tasks.json diff=fixture filter=fixture\n"))
	before := boardSnapshot(t, root)
	data, _, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, "ancestor board", data.Nodes[0].Title)
	require.NoFileExists(t, marker)
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_EnvironmentPolicy_DiscardsAmbientOverrides(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	marker := filepath.Join(t.TempDir(), "canary")
	script := boardScript(t, "printf observed > '"+marker+"'")
	boardPositiveControl(t, script, marker)
	config := filepath.Join(t.TempDir(), "config")
	boardWrite(t, config, []byte("[core]\nfsmonitor = "+script+"\n[credential]\nhelper = !"+script+"\n"))
	for key, value := range map[string]string{"GIT_CONFIG_GLOBAL": config, "GIT_CONFIG_SYSTEM": config, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.fsmonitor", "GIT_CONFIG_VALUE_0": script, "GIT_DIR": "/absent", "GIT_WORK_TREE": "/absent", "GIT_ASKPASS": script, "GIT_TRACE": marker, "GIT_EXEC_PATH": "/absent", "GIT_ALLOW_PROTOCOL": "file", "HOME": filepath.Dir(config), "XDG_CONFIG_HOME": filepath.Dir(config)} {
		t.Setenv(key, value)
	}
	before := boardSnapshot(t, root)
	data, _, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, "ancestor board", data.Nodes[0].Title)
	require.NoFileExists(t, marker)
	require.Equal(t, before, boardSnapshot(t, root))
	require.Contains(t, boardGitEnv(), "GIT_ALLOW_PROTOCOL=")
	require.Contains(t, boardGitArgs(nil), "credential.helper=")
	require.NotContains(t, boardGitArgs(nil), "credential.helper=.")
}

func TestMergeBaseBoard_ReplacementObjects_DoNotChangeBoard(t *testing.T) {
	root, _, oid := boardPullFixture(t)
	head := strings.TrimSpace(boardGit(t, root, "rev-parse", "HEAD"))
	boardGit(t, root, "replace", oid, head)
	before := boardSnapshot(t, root)
	data, p, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, "ancestor board", data.Nodes[0].Title)
	require.Equal(t, oid, p.BaseOID)
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_PromisorObjects_DoNotFetch(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	blobOID := strings.TrimSpace(boardGit(t, root, "rev-parse", "HEAD:.mtix/tasks.json"))
	marker := filepath.Join(t.TempDir(), "fetch-canary")
	helper := boardScript(t, "printf observed > '"+marker+"'; exit 1")
	boardPositiveFailure(t, helper, marker)
	boardGit(t, root, "config", "remote.origin.promisor", "true")
	boardGit(t, root, "config", "remote.origin.partialclonefilter", "blob:none")
	boardGit(t, root, "config", "remote.origin.url", "ext::"+helper)
	boardGit(t, root, "config", "protocol.ext.allow", "always")
	require.NoError(t, os.Remove(filepath.Join(root, ".git", "objects", blobOID[:2], blobOID[2:])))
	before := boardSnapshot(t, root)
	data, _, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.Nil(t, data)
	require.NoFileExists(t, marker)
	require.Equal(t, before, boardSnapshot(t, root))
}

func boardPositiveFailure(t *testing.T, path, marker string) {
	t.Helper()
	ctx, cancel := boardTestWatchdog(t)
	defer cancel()
	err := exec.CommandContext(ctx, path).Run()
	require.NoError(t, ctx.Err(), "negative canary watchdog expired")
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 1, exitErr.ExitCode(), "negative canary must finish its intended exit")
	require.FileExists(t, marker)
	require.NoError(t, os.Remove(marker))
}
