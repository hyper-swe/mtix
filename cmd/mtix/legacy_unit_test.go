// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lookup of a 0.5.x per-project unit installed through a symlinked project root (ADR-007 D11).
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// installUnit writes the unit that `mtix daemon install` would write for root as opened.
func installUnit(t *testing.T, goos, root, home string) {
	t.Helper()
	spec, err := buildServiceSpec(goos, "mtix", root, home, daemonDispatchDefaultIntervalSec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
	require.NoError(t, os.WriteFile(spec.Path, []byte(spec.Content), 0o600))
}

func TestLegacyUnitInstalled_SymlinkedProjectRoot_FoundUnderCanonicalRoot(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			canonical, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			link := filepath.Join(t.TempDir(), "checkout-link")
			require.NoError(t, os.Symlink(canonical, link))
			otherRoot, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)

			installed, err := legacyUnitInstalled(goos, canonical, home)
			require.NoError(t, err)
			require.False(t, installed, "no unit yet")

			installUnit(t, goos, otherRoot, home)
			installed, err = legacyUnitInstalled(goos, canonical, home)
			require.NoError(t, err)
			require.False(t, installed, "another project's unit does not count")

			installUnit(t, goos, link, home) // installed from the project as opened through the symlink
			installed, err = legacyUnitInstalled(goos, canonical, home)
			require.NoError(t, err)
			require.True(t, installed, "the host sees the canonical root; the unit hashes the symlinked one")
		})
	}
}

func TestLegacyUnitInstalled_UnrelatedFileWithSamePrefix_Ignored(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	spec, err := buildServiceSpec("linux", "mtix", root, home, daemonDispatchDefaultIntervalSec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
	stray := filepath.Join(filepath.Dir(spec.Path), "mtix-daemon-stray.service")
	require.NoError(t, os.WriteFile(stray, []byte("[Service]\nExecStart=/bin/true\n"), 0o600))
	installed, err := legacyUnitInstalled("linux", root, home)
	require.NoError(t, err)
	require.False(t, installed)
}

func TestLegacyServiceProbe_SymlinkedInstall_OwnsDutyWhileDaemonDown(t *testing.T) {
	home := t.TempDir()
	canonical, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "checkout-link")
	require.NoError(t, os.Symlink(canonical, link))
	installUnit(t, "linux", link, home)
	mtixDir := filepath.Join(canonical, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o700))
	probe := legacyServiceProbe("linux", func() (string, error) { return home, nil })
	live, err := probe(t.Context(), mtixDir)
	require.NoError(t, err)
	require.True(t, live, "the supervisor restart gap stays closed for a symlinked project")
}
