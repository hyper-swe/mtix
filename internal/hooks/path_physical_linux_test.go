// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareExec_PerProcessLinkPathsAreRefused(t *testing.T) {
	for _, command := range []string{
		"[sh, /proc/self/cwd/scripts/later.sh]",
		"[/proc/self/cwd/scripts/run.sh]",
		"[sh, /proc/thread-self/cwd/scripts/later.sh]",
		"[sh, scripts/run.sh, /proc/self/root/etc/hostname]",
	} {
		for shape, dir := range map[string]func(*testing.T, prepareFixture) string{
			"checker outside the project": func(t *testing.T, _ prepareFixture) string { return outsideDir(t) },
			"checker in the project root": func(_ *testing.T, f prepareFixture) string { return f.root },
		} {
			t.Run(shape+" "+command, func(t *testing.T) {
				f := newFixture(t, command, fixtureOptions{})
				t.Chdir(dir(t, f))                                                                                            // the launch always runs from the root
				require.NoError(t, os.WriteFile(filepath.Join(f.root, "scripts", "later.sh"), []byte("echo later\n"), 0o700)) //nolint:gosec // test file
				plan, err := f.approvals.PrepareExec(f.hook(t))
				require.Error(t, err)
				require.Nil(t, plan)
			})
		}
	}
}

func TestPrepareExec_StandardStreamPathsKeepTheirBaseBehaviour(t *testing.T) {
	f := newFixture(t, "[sh, scripts/run.sh, /dev/stdin, --log=/dev/stderr, /dev/fd/0, /proc/self/status, /proc/self/exe]", fixtureOptions{})
	t.Chdir(outsideDir(t))
	plan, err := f.approvals.PrepareExec(f.hook(t))
	require.NoError(t, err, "a route that does not resolve into the project is an ordinary outside path")
	require.NotNil(t, plan)
}

func TestUnstableLink_IsDecidedByTheFilesystem(t *testing.T) {
	require.True(t, unstableLink("/proc/self/cwd"))
	require.True(t, unstableLink("/proc/self"))
	dir := outsideDir(t)
	link := filepath.Join(dir, "plain")
	require.NoError(t, os.Symlink(dir, link))
	require.False(t, unstableLink(link))
}
