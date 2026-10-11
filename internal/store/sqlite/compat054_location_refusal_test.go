// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Invalid local fixtures test refusal only; real cached readers prove compatibility.

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompat054ReaderLocation_NoRepositoryOrValidCache_ReturnsClearError(t *testing.T) {
	for _, kind := range []string{"Missing", "MissingChecksum", "InvalidChecksum"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			cache := t.TempDir()
			platform := runtime.GOOS + "-" + runtime.GOARCH
			if kind != "Missing" {
				name := "mtix"
				if runtime.GOOS == "windows" {
					name += ".exe"
				}
				path := filepath.Join(cache, "mtix-test", "mtix-v0.5.4-beta", compat054TagCommit, platform, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, []byte("invalid-reader-fixture"), 0700))
				if kind == "InvalidChecksum" {
					require.NoError(t, os.WriteFile(path+".sha256", []byte(strings.Repeat("0", 64)), 0600))
				}
			}
			cached, repo, err := compat054ReaderLocation(root, cache, platform)
			assert.Error(t, err)
			assert.Empty(t, cached)
			assert.Empty(t, repo)
			if err != nil {
				assert.Contains(t, err.Error(), "cache missing or invalid")
				assert.Contains(t, err.Error(), "tag repository unavailable")
			}
		})
	}
}
