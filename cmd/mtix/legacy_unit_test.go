// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lookup of a 0.5.x per-project unit installed through a symlinked project root (ADR-007 D11).
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
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

			installed, _, err := legacyUnitInstalled(goos, canonical, home)
			require.NoError(t, err)
			require.False(t, installed, "no unit yet")

			installUnit(t, goos, otherRoot, home)
			installed, _, err = legacyUnitInstalled(goos, canonical, home)
			require.NoError(t, err)
			require.False(t, installed, "another project's unit does not count")

			installUnit(t, goos, link, home) // installed from the project as opened through the symlink
			installed, _, err = legacyUnitInstalled(goos, canonical, home)
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
	installed, _, err := legacyUnitInstalled("linux", root, home)
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

// unitDirFor returns the user unit directory a linux install for root writes into.
func unitDirFor(t *testing.T, root, home string) string {
	t.Helper()
	spec, err := buildServiceSpec("linux", "mtix", root, home, daemonDispatchDefaultIntervalSec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
	return filepath.Dir(spec.Path)
}

func TestLegacyUnitInstalled_UnreadableSiblings_SkippedAndReported(t *testing.T) {
	home := t.TempDir()
	canonical, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "checkout-link")
	require.NoError(t, os.Symlink(canonical, link))
	dir := unitDirFor(t, canonical, home)
	dangling := filepath.Join(dir, "mtix-daemon-dangling.service")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "gone"), dangling))
	isDir := filepath.Join(dir, "mtix-daemon-adir.service")
	require.NoError(t, os.Mkdir(isDir, 0o700))

	installed, skipped, err := legacyUnitInstalled("linux", canonical, home)
	require.NoError(t, err, "stray entries never fail the lookup")
	require.False(t, installed)
	require.ElementsMatch(t, []string{dangling, isDir}, skipped)

	installUnit(t, "linux", link, home) // a readable sibling that serves this project is still found
	installed, skipped, err = legacyUnitInstalled("linux", canonical, home)
	require.NoError(t, err)
	require.True(t, installed, "other entries do not hide the unit that serves this board")
	require.ElementsMatch(t, []string{dangling, isDir}, skipped)
}

func TestLegacyUnitInstalled_OwnUnitUnreadable_CountsAsInstalled(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	dir := unitDirFor(t, root, home)
	spec, err := buildServiceSpec("linux", "mtix", root, home, daemonDispatchDefaultIntervalSec)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(filepath.Join(dir, "missing-target"), spec.Path)) // dangling, but this board's own name
	installed, skipped, err := legacyUnitInstalled("linux", root, home)
	require.NoError(t, err)
	require.True(t, installed)
	require.Empty(t, skipped)
}

func TestRegisterLegacyServiceDoctor_UnreadableSibling_ReportsRowWithoutFailing(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o700))
	dir := unitDirFor(t, root, home)
	dangling := filepath.Join(dir, "mtix-daemon-dangling.service")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "gone"), dangling))
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerLegacyServiceDoctor(svc, mtixDir, "linux", home))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
		if row.ID == "dispatcher.legacy-unit-scan" {
			require.Equal(t, service.DoctorWarn, row.Status)
			require.NotEmpty(t, row.Fix)
			require.Equal(t, []string{dangling}, row.Data["unreadable_units"])
		}
	}
	require.Contains(t, ids, "dispatcher.legacy-unit-scan")
	require.NotContains(t, ids, "dispatcher.legacy-service", "an unreadable stray is not a legacy service")
}
