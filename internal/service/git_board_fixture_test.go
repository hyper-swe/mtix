// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Owned local Git fixtures exercise committed-board reads without live remotes.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func boardGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native Git policy is unavailable on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_TERMINAL_PROMPT=0"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "owned Git setup: %s", out)
	return string(out)
}

func boardWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, data, 0600))
}

func boardBytes(t *testing.T, title string) []byte {
	t.Helper()
	raw := fmt.Sprintf(`{"version":1,"schema_version":"2.0.0","mtix_version":"0.5.4","project":"PROJ","nodes":[{"id":"PROJ-1","title":%q,"created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-01T00:00:00Z","status":"open","labels":"[]","uid":"018f2892-3757-7b5a-9b2c-3e05c2a77290"}],"dependencies":[],"agents":[],"sessions":[],"node_count":1}`, title)
	data, err := sqlite.DecodeExportData(strings.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	out, err := json.Marshal(data)
	require.NoError(t, err)
	return out
}

// boardPullFixture returns a checkout after a real fast-forward pull and its pre-pull board.
func boardPullFixture(t *testing.T) (string, []byte, string) {
	t.Helper()
	base := t.TempDir()
	remote, writer, reader := filepath.Join(base, "remote.git"), filepath.Join(base, "writer"), filepath.Join(base, "reader")
	boardGit(t, base, "init", "--bare", remote)
	boardGit(t, base, "clone", remote, writer)
	boardGit(t, writer, "checkout", "-b", "main")
	old := boardBytes(t, "ancestor board")
	boardWrite(t, filepath.Join(writer, ".mtix", "tasks.json"), old)
	boardGit(t, writer, "add", ".mtix/tasks.json")
	boardGit(t, writer, "commit", "-m", "fixture base")
	oid := strings.TrimSpace(boardGit(t, writer, "rev-parse", "HEAD"))
	boardGit(t, writer, "push", "-u", "origin", "main")
	boardGit(t, base, "clone", "--branch", "main", remote, reader)
	boardWrite(t, filepath.Join(writer, ".mtix", "tasks.json"), boardBytes(t, "received board"))
	boardGit(t, writer, "commit", "-am", "fixture next")
	boardGit(t, writer, "push")
	boardGit(t, reader, "pull", "--ff-only")
	require.Equal(t, oid, strings.TrimSpace(boardGit(t, reader, "rev-parse", "ORIG_HEAD")))
	require.NotEqual(t, oid, strings.TrimSpace(boardGit(t, reader, "rev-parse", "HEAD")))
	return reader, old, oid
}

func boardSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = fmt.Sprintf("%x", sha256.Sum256(bytes))
		return nil
	}))
	return result
}

func boardSingleRepo(t *testing.T, blob []byte) string {
	t.Helper()
	root := t.TempDir()
	boardGit(t, root, "init", "-b", "main")
	boardWrite(t, filepath.Join(root, ".mtix", "tasks.json"), blob)
	boardGit(t, root, "add", ".mtix/tasks.json")
	boardGit(t, root, "commit", "-m", "fixture base")
	boardGit(t, root, "update-ref", "ORIG_HEAD", "HEAD")
	boardGit(t, root, "update-ref", "refs/remotes/origin/main", "HEAD")
	boardGit(t, root, "config", "remote.origin.url", "file:///unavailable")
	boardGit(t, root, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	boardGit(t, root, "config", "branch.main.remote", "origin")
	boardGit(t, root, "config", "branch.main.merge", "refs/heads/main")
	return root
}
func boardTrustedScript(t *testing.T, path string) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native Git process policy unavailable")
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(path))
	require.NoError(t, err)
	trusted, err := trustedBoardGit(path, root)
	require.NoError(t, err)
	return trusted
}
