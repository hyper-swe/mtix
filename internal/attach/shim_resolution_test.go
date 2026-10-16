// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package attach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShim_ExecsThePhysicallyResolvedPath pins that the shim runs the path it
// checked, with every directory link resolved, so swapping a link between the
// check and the exec cannot redirect it; the Go mirror resolves the same path.
func TestShim_ExecsThePhysicallyResolvedPath(t *testing.T) {
	f := newShimFixture(t)
	require.NoError(t, os.WriteFile(f.target, []byte("#!/bin/sh\nprintf '%s\\n' \"$0\"\n"), 0o700)) //nolint:gosec // fixture script
	d := filepath.Join(f.dir, "D")
	require.NoError(t, os.Mkdir(d, 0o700))
	makeLink(t, d, "x", filepath.Dir(f.target))
	entry := makeLink(t, f.dir, "entry", filepath.Join(d, "x", "mtix"))
	stdout, stderr, exit, goErr := f.run(t, entry, os.Getuid())
	require.Equal(t, 0, exit, stderr)
	assert.Equal(t, f.target+"\n", stdout, "the shim execs the physical path, not one through a link")
	require.NoError(t, goErr)
	resolved, err := ValidateShimTarget(entry, os.Getuid())
	require.NoError(t, err)
	assert.Equal(t, f.target, resolved, "sh and Go resolve the same file")
}

// uniqueTmpName names a /tmp entry no other test process uses: the parent
// of t.TempDir() is unique per test and run.
func uniqueTmpName(prefix string, f shimFixture) string {
	return prefix + "-" + filepath.Base(filepath.Dir(f.dir))
}

// rootStickyTmp returns /tmp when it is a root-owned sticky directory.
func rootStickyTmp(t *testing.T) string {
	t.Helper()
	info, err := os.Stat("/tmp")
	if err != nil || info.Mode()&os.ModeSticky == 0 || ownerOf(info) != 0 {
		t.Skip("this host has no root-owned sticky /tmp")
	}
	return "/tmp"
}

// TestShim_ForeignLinkInRootStickyDirectory_Refused covers the link-owner
// rule: in a root-owned sticky directory anyone may plant a link, so a link
// owned by another user is refused even when it points at a root-owned file.
func TestShim_ForeignLinkInRootStickyDirectory_Refused(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns every fixture link")
	}
	tmp := rootStickyTmp(t)
	f := newShimFixture(t)
	link := filepath.Join(tmp, uniqueTmpName("mtix-shim-foreign", f))
	require.NoError(t, os.Symlink("/bin/sh", link))
	t.Cleanup(func() { _ = os.Remove(link) }) //nolint:errcheck // best-effort fixture cleanup
	// The shim is written for another uid, so this test's own link is foreign.
	stdout, stderr, exit, goErr := f.run(t, link, os.Getuid()+1, "-c", "echo RAN-FOREIGN")
	assert.Equal(t, 126, exit, stderr)
	assert.NotContains(t, stdout, "RAN-FOREIGN")
	assert.Contains(t, stderr, "neither the operator nor root")
	require.ErrorIs(t, goErr, ErrShimTargetRefused)
	var refusal *ShimRefusal
	require.True(t, errors.As(goErr, &refusal))
	physTmp, err := filepath.EvalSymlinks(tmp)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(physTmp, filepath.Base(link)), refusal.Path, "the refusal names the physical link")
	assert.Equal(t, RefusedOwner, refusal.Reason)
}

// TestShimWriterCheck_Refusal_FixNamesTheRefusedPath pins the exact-fix rule:
// the setup.shim fix names the component that was refused.
func TestShimWriterCheck_Refusal_FixNamesTheRefusedPath(t *testing.T) {
	tests := []struct {
		name    string
		target  func(f shimFixture) string
		prepare func(t *testing.T, f shimFixture) string // returns the refused path
	}{
		{"writable ancestor", func(f shimFixture) string { return f.target }, func(t *testing.T, f shimFixture) string {
			require.NoError(t, os.Chmod(f.dir, 0o777))
			return f.dir
		}},
		{"writable target", func(f shimFixture) string { return f.target }, func(t *testing.T, f shimFixture) string {
			require.NoError(t, os.Chmod(f.target, 0o777))
			return f.target
		}},
		{"writable directory holding a link", func(f shimFixture) string { return filepath.Join(f.dir, "L", "mtix") }, func(t *testing.T, f shimFixture) string {
			l := filepath.Join(f.dir, "L")
			require.NoError(t, os.Chmod(l, 0o777))
			return l
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Getuid() == 0 {
				t.Skip("root owns every fixture directory")
			}
			f := newShimFixture(t)
			t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) }) //nolint:errcheck // best-effort fixture cleanup
			require.NoError(t, os.Mkdir(filepath.Join(f.dir, "L"), 0o700))
			makeLink(t, filepath.Join(f.dir, "L"), "mtix", f.target)
			st := newMemoryStore(filepath.Join(f.dir, "op"))
			w := NewShimWriter(st, Protocol, ValidateShimTarget)
			_, err := w.Install(context.Background(), tt.target(f), os.Getuid())
			require.NoError(t, err)
			refused := tt.prepare(t, f)
			status, err := w.Check(context.Background())
			require.NoError(t, err)
			assert.Equal(t, ArtifactRefused, status.State)
			assert.Equal(t, "chmod go-w "+refused, status.Fix)
		})
	}
}
