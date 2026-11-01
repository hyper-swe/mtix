// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Cached-reader tests execute the same compiled fixture outside a repository.

import (
	"crypto/sha256"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func TestCompat054Binary_NoRepositoryWithValidCache_UsesRealReader(t *testing.T) {
	prepared := compat054Binary(t)
	cache, err := hostguard.RealCacheDir()
	require.NoError(t, err)
	commit := "5e9b641b36f37e5deb5a99fbee196d0d8a8ba2b1"
	name := "mtix"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(cache, "mtix-test", "mtix-v0.5.4-beta", commit, runtime.GOOS+"-"+runtime.GOARCH, name)
	require.Equal(t, path, prepared)
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "actual tagged reader cache required")
	sum, err := os.ReadFile(path + ".sha256")
	require.NoError(t, err)
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	require.Equal(t, strings.TrimSpace(string(sum)), digest)
	t.Logf("actual cached reader commit=%s sha256=%s", commit, digest)
	root := t.TempDir()
	for dir := root; ; dir = filepath.Dir(dir) {
		_, err = os.Stat(filepath.Join(dir, ".git"))
		require.ErrorIs(t, err, os.ErrNotExist)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	binary, err := os.Executable()
	require.NoError(t, err)
	compiled, err := os.ReadFile(binary)
	require.NoError(t, err)
	t.Logf("compiled source-bound fixture sha256=%x", sha256.Sum256(compiled))
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestCompat054Binary_CachedLocation_ChildProcess$", "-test.v")
	cmd.Dir = root
	cmd.Env = append(hostguard.Environ(), "MTIX_COMPAT_LOCATION_CHILD=1")
	output, err := cmd.CombinedOutput()
	t.Logf("owned compiled reader-fixture child pid=%d exit=%d", cmd.Process.Pid, cmd.ProcessState.ExitCode())
	assert.NoError(t, err, "cached reader fixture: %s", output)
	assert.Contains(t, string(output), "cached-reader-path="+path)
}

func TestCompat054Binary_CachedLocation_ChildProcess(t *testing.T) {
	if os.Getenv("MTIX_COMPAT_LOCATION_CHILD") != "1" {
		return
	}
	path := compat054Binary(t)
	t.Logf("cached-reader-path=%s", path)
	TestSchema_WakeLeasesAdditive_054BinaryOpensStore(t)
}
