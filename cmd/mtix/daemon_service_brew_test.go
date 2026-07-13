// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-56.11: a service must never be registered against a version-scoped
// Homebrew Cellar path — brew cleanup deletes it after an upgrade and the
// crash-restart loop respawns a missing file.
func TestStableExecutablePath_MapsCellarToOpt(t *testing.T) {
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", "mtix", "1.2.3", "bin")
	opt := filepath.Join(prefix, "opt", "mtix", "bin")
	require.NoError(t, os.MkdirAll(cellar, 0o755))
	require.NoError(t, os.MkdirAll(opt, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellar, "mtix"), []byte("x"), 0o755)) //nolint:gosec
	require.NoError(t, os.WriteFile(filepath.Join(opt, "mtix"), []byte("x"), 0o755))    //nolint:gosec

	got := stableExecutablePath(filepath.Join(cellar, "mtix"))
	assert.Equal(t, filepath.Join(opt, "mtix"), got,
		"a Cellar path maps to the upgrade-stable opt symlink")
}

func TestStableExecutablePath_PassthroughOtherwise(t *testing.T) {
	assert.Equal(t, "/usr/local/bin/mtix", stableExecutablePath("/usr/local/bin/mtix"),
		"non-brew paths are untouched")
	assert.Equal(t, "/x/Cellar/weird", stableExecutablePath("/x/Cellar/weird"),
		"a Cellar-ish path without the full layout is untouched")
	assert.Equal(t, "/x/Cellar/mtix/9.9/bin/mtix", stableExecutablePath("/x/Cellar/mtix/9.9/bin/mtix"),
		"no real opt counterpart on disk -> keep what we were given")
}
