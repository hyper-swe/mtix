// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// The compatibility fixture builds a real tagged reader; a build failure is a test failure.

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
)

func compat054Repository(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		metadata := filepath.Join(dir, ".git")
		if info, err := os.Stat(metadata); err == nil {
			if info.IsDir() {
				return dir
			}
			raw, err := os.ReadFile(metadata)
			require.NoError(t, err)
			if target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: "); ok {
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, target)
				}
				if info, err := os.Stat(target); err == nil && info.IsDir() {
					return dir
				}
			}
		} else {
			require.ErrorIs(t, err, os.ErrNotExist)
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "real compatibility tag repository not found")
		dir = parent
	}
}

func compat054Command(t *testing.T, dir string, command string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), command, args...)
	cmd.Dir = dir
	if command == "git" {
		// Read-only canonical tag operations must not inherit worktree selection.
		for _, value := range os.Environ() {
			key, _, _ := strings.Cut(value, "=")
			if key != "GIT_DIR" && key != "GIT_WORK_TREE" && key != "GIT_COMMON_DIR" && key != "GIT_INDEX_FILE" {
				cmd.Env = append(cmd.Env, value)
			}
		}
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "real reader command %s %v: %s", command, args, output)
	return output
}

func compat054Binary(t *testing.T) string {
	t.Helper()
	repo := compat054Repository(t)
	commit := strings.TrimSpace(string(compat054Command(t, repo, "git", "rev-parse", "v0.5.4-beta^{commit}")))
	require.Regexp(t, "^[0-9a-f]{40}$", commit)
	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	dir := filepath.Join(cache, "mtix-test", "mtix-v0.5.4-beta", commit, runtime.GOOS+"-"+runtime.GOARCH)
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
		require.Equal(t, hex.EncodeToString(digest[:]), strings.TrimSpace(string(sum)))
		t.Logf("real reader cached commit=%s sha256=%x", commit, digest)
		return path
	}
	require.True(t, errors.Is(readErr, os.ErrNotExist), "read real reader cache: %v", readErr)
	return buildCompat054(t, repo, commit, dir, path)
}

func buildCompat054(t *testing.T, repo, commit, cache, path string) string {
	t.Helper()
	dir := t.TempDir()
	archive := compat054Command(t, repo, "git", "archive", commit)
	require.NoError(t, extractCompat054(dir, archive))
	output := filepath.Join(dir, "reader")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", output, "./cmd/mtix")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	log, err := cmd.CombinedOutput()
	require.NoError(t, err, "real v0.5.4 tag build failed (report actual failure; never skip): %s", log)
	raw, err := os.ReadFile(output)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	require.NoError(t, os.MkdirAll(cache, 0700))
	stage, err := os.CreateTemp(cache, ".reader-")
	require.NoError(t, err)
	stagePath := stage.Name()
	t.Cleanup(func() {
		if err := os.Remove(stagePath); err != nil {
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	})
	_, err = stage.Write(raw)
	closeErr := stage.Close()
	require.NoError(t, errors.Join(err, closeErr))
	require.NoError(t, os.Chmod(stagePath, 0700))
	require.NoError(t, os.WriteFile(path+".sha256", []byte(hex.EncodeToString(digest[:])+"\n"), 0600))
	require.NoError(t, os.Rename(stagePath, path))
	t.Logf("real reader built commit=%s sha256=%x GOTOOLCHAIN=local GOFLAGS=-mod=mod", commit, digest)
	return path
}

func extractCompat054(root string, raw []byte) error {
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
		case tar.TypeXGlobalHeader:
			// Git records its commit in a PAX metadata header, not a file.
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			if err := writeCompat054File(path, os.FileMode(header.Mode)&0777, reader); err != nil {
				return err
			}
		default:
			return errors.New("unsupported tag archive entry")
		}
	}
}

func writeCompat054File(path string, mode os.FileMode, reader io.Reader) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, reader)
	return errors.Join(err, file.Close())
}
