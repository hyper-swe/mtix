// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

// Real released mtix binaries, built from their git tags (never reconstructed
// by hand), drive the old side of the upgrade tests; a build failure is a
// test failure. The binary is cached under the user cache directory by tag
// commit and verified by checksum, as the compat054 fixture does.

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// The released tags the upgrade tests run and the commits they must resolve to.
const (
	tag053       = "v0.5.3-beta"
	tag053Commit = "d6cd8a12fefe1d35c622ebdcbf5973964b4442fb"
	tag030       = "v0.3.0-beta"
	tag030Commit = "1686749fa36c4d2b98d731f79dbc6bce48378361"
)

// cleanGitEnv returns the environment without variables that select a worktree.
func cleanGitEnv() []string {
	var env []string
	for _, value := range hostguard.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "GIT_DIR" && key != "GIT_WORK_TREE" && key != "GIT_COMMON_DIR" && key != "GIT_INDEX_FILE" {
			env = append(env, value)
		}
	}
	return env
}

// releasedMtix returns the path of the mtix binary built from tag, whose
// commit must be commit.
func releasedMtix(t *testing.T, tag, commit string) string {
	t.Helper()
	cache, err := hostguard.RealCacheDir()
	require.NoError(t, err)
	dir := filepath.Join(cache, "mtix-test", "mtix-"+tag, commit, runtime.GOOS+"-"+runtime.GOARCH)
	name := "mtix"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	raw, readErr := os.ReadFile(path)
	if readErr == nil {
		sum, err := os.ReadFile(path + ".sha256")
		require.NoError(t, err)
		digest := sha256.Sum256(raw)
		require.Equal(t, hex.EncodeToString(digest[:]), strings.TrimSpace(string(sum)), "released %s binary cache", tag)
		return path
	}
	require.True(t, errors.Is(readErr, os.ErrNotExist), "read the %s cache: %v", tag, readErr)
	return buildReleasedMtix(t, tag, commit, dir, path)
}

// buildReleasedMtix builds the tag from a git archive of its commit.
func buildReleasedMtix(t *testing.T, tag, commit, cacheDir, path string) string {
	t.Helper()
	repo := findGitRepository(t)
	resolved := strings.TrimSpace(runIn(t, repo, cleanGitEnv(), "git", "rev-parse", tag+"^{commit}"))
	require.Equal(t, commit, resolved, "tag %s moved", tag)
	src := t.TempDir()
	archive := runIn(t, repo, cleanGitEnv(), "git", "archive", commit)
	require.NoError(t, extractTar(src, []byte(archive)))
	out := filepath.Join(src, "released-mtix")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", out, "./cmd/mtix")
	cmd.Dir = src
	cmd.Env = append(hostguard.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	log, err := cmd.CombinedOutput()
	require.NoError(t, err, "real %s tag build failed (report the failure; never skip): %s", tag, log)
	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	require.NoError(t, os.MkdirAll(cacheDir, 0o700))
	require.NoError(t, os.WriteFile(path+".sha256", []byte(hex.EncodeToString(digest[:])+"\n"), 0o600))
	require.NoError(t, os.WriteFile(path, raw, 0o700))
	return path
}

// findGitRepository finds the checkout holding the release tags.
func findGitRepository(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for ; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		require.NotEqual(t, filepath.Dir(dir), dir, "the released binary is not cached and no git checkout holds its tag")
	}
}

// runIn runs a command in dir and returns its combined output.
func runIn(t *testing.T, dir string, env []string, command string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), command, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s %v: %s", command, args, out)
	return string(out)
}

// extractTar unpacks a git archive into root (regular files and directories).
func extractTar(root string, raw []byte) error {
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if !filepath.IsLocal(header.Name) {
			return errors.New("tag archive path is not local")
		}
		path := filepath.Join(root, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			body, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, body, os.FileMode(header.Mode)&0o700|0o600); err != nil {
				return err
			}
		}
	}
}
