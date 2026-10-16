// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package attach

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shimFixture is a private directory holding a fake mtix target and a shim.
type shimFixture struct {
	dir    string
	target string
	cwd    string // working directory the shim runs in; empty keeps the test's
}

func newShimFixture(t *testing.T) shimFixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	target := filepath.Join(bin, "mtix")
	script := "#!/bin/sh\nprintf 'ran:%s|path:%s\\n' \"$*\" \"$PATH\"\n"
	require.NoError(t, os.WriteFile(target, []byte(script), 0o700)) //nolint:gosec // test executable in a private temp dir
	return shimFixture{dir: dir, target: target}
}

// run writes a shim recording target for uid, executes it, and also runs the
// Go validator so both sides of the rule are judged by the same case.
func (f shimFixture) run(t *testing.T, target string, uid int, args ...string) (stdout, stderr string, exit int, goErr error) {
	t.Helper()
	data, err := ShimScript(ShimRecord{Target: target, UID: uid, Protocol: Protocol})
	require.NoError(t, err)
	shim := filepath.Join(f.dir, "mtix-shim")
	require.NoError(t, os.WriteFile(shim, data, 0o700)) //nolint:gosec // the shim is an executable by design
	cmd := exec.Command(shim, args...)                  //nolint:gosec // fixture path
	cmd.Env = []string{"PATH=/caller/bin:/usr/bin:/bin"}
	cmd.Dir = f.cwd
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	var exitErr *exec.ExitError
	if runErr != nil {
		require.True(t, errors.As(runErr, &exitErr), "shim did not start: %v", runErr)
		exit = exitErr.ExitCode()
	}
	_, goErr = ValidateShimTarget(target, uid)
	return out.String(), errOut.String(), exit, goErr
}

func TestShim_TargetNotOwnedByUser_Refused(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root owns every fixture file, so no file is owned by another user")
	}
	f := newShimFixture(t)
	stdout, stderr, exit, goErr := f.run(t, f.target, os.Getuid()+1, "inbox")
	assert.Equal(t, 126, exit, stderr)
	assert.Empty(t, stdout, "the refused target never runs")
	assert.Contains(t, stderr, "neither the operator nor root")
	require.Error(t, goErr)
	assert.ErrorIs(t, goErr, ErrShimTargetRefused)
	assert.Contains(t, goErr.Error(), "neither the operator nor root")
}

func TestShim_OwnedTarget_ExecsWithArgsAndCallerPATH(t *testing.T) {
	f := newShimFixture(t)
	stdout, stderr, exit, goErr := f.run(t, f.target, os.Getuid(), "attach", "session-start")
	assert.Equal(t, 0, exit, stderr)
	assert.Equal(t, "ran:attach session-start|path:/caller/bin:/usr/bin:/bin\n", stdout, "the target gets the caller's PATH back")
	assert.NoError(t, goErr)
}

func TestShim_TargetAndParentModes_RefusedWhenWritableByOthers(t *testing.T) {
	tests := []struct {
		name       string
		file, dir  os.FileMode
		wantReason string
	}{
		{"group-writable target", 0o770, 0o700, "writable by group or others"},
		{"world-writable target", 0o707, 0o700, "writable by group or others"},
		{"group-writable parent", 0o700, 0o770, "writable by group or others"},
		{"world-writable parent", 0o700, 0o707, "writable by group or others"},
		{"not executable", 0o600, 0o700, "not executable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShimFixture(t)
			require.NoError(t, os.Chmod(f.target, tt.file))
			require.NoError(t, os.Chmod(filepath.Dir(f.target), tt.dir))
			t.Cleanup(func() { _ = os.Chmod(filepath.Dir(f.target), 0o700) }) //nolint:errcheck // best-effort fixture cleanup
			stdout, stderr, exit, goErr := f.run(t, f.target, os.Getuid())
			assert.Equal(t, 126, exit, stderr)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tt.wantReason)
			require.ErrorIs(t, goErr, ErrShimTargetRefused)
			assert.Contains(t, goErr.Error(), tt.wantReason)
		})
	}
}

func TestShim_SymbolicLinks_ResolvedThenValidated(t *testing.T) {
	tests := []struct {
		name     string
		link     func(t *testing.T, f shimFixture) string
		wantExit int
		wantText string
	}{
		{"absolute link to a valid target", func(t *testing.T, f shimFixture) string {
			return makeLink(t, f.dir, "abs", f.target)
		}, 0, "ran:"},
		{"relative link chain to a valid target", func(t *testing.T, f shimFixture) string {
			first := makeLink(t, f.dir, "first", "bin/mtix")
			return makeLink(t, f.dir, "second", filepath.Base(first))
		}, 0, "ran:"},
		{"link to a group-writable target", func(t *testing.T, f shimFixture) string {
			require.NoError(t, os.Chmod(f.target, 0o770))
			return makeLink(t, f.dir, "abs", f.target)
		}, 126, "writable by group or others"},
		{"dangling link", func(t *testing.T, f shimFixture) string {
			return makeLink(t, f.dir, "dangling", filepath.Join(f.dir, "absent"))
		}, 126, "not a regular file"},
		{"link loop", func(t *testing.T, f shimFixture) string {
			a := filepath.Join(f.dir, "loop-a")
			require.NoError(t, os.Symlink(filepath.Join(f.dir, "loop-b"), a))
			require.NoError(t, os.Symlink(a, filepath.Join(f.dir, "loop-b")))
			return a
		}, 126, "more than 32 symbolic links"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newShimFixture(t)
			target := tt.link(t, f)
			stdout, stderr, exit, goErr := f.run(t, target, os.Getuid())
			assert.Equal(t, tt.wantExit, exit, stderr)
			if tt.wantExit == 0 {
				assert.Contains(t, stdout, tt.wantText)
				assert.NoError(t, goErr)
				return
			}
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, tt.wantText)
			require.ErrorIs(t, goErr, ErrShimTargetRefused)
			assert.Contains(t, goErr.Error(), tt.wantText)
		})
	}
}

func TestValidateShimTarget_ResolvedPath_IsTheFinalFile(t *testing.T) {
	f := newShimFixture(t)
	link := makeLink(t, f.dir, "abs", f.target)
	resolved, err := ValidateShimTarget(link, os.Getuid())
	require.NoError(t, err)
	assert.Equal(t, f.target, resolved)
}

func TestShimScript_InvalidRecord_Refused(t *testing.T) {
	tests := []struct {
		name string
		rec  ShimRecord
	}{
		{"relative target", ShimRecord{Target: "bin/mtix", UID: 1, Protocol: 1}},
		{"empty target", ShimRecord{Target: "", UID: 1, Protocol: 1}},
		{"quote in target", ShimRecord{Target: "/opt/it's/mtix", UID: 1, Protocol: 1}},
		{"newline in target", ShimRecord{Target: "/opt/a\nb/mtix", UID: 1, Protocol: 1}},
		{"negative uid", ShimRecord{Target: "/opt/mtix", UID: -1, Protocol: 1}},
		{"protocol zero", ShimRecord{Target: "/opt/mtix", UID: 1, Protocol: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ShimScript(tt.rec)
			assert.Error(t, err)
		})
	}
}

func TestShimScript_Bytes_RecordUnresolvedPathUIDAndProtocol(t *testing.T) {
	data, err := ShimScript(ShimRecord{Target: "/opt/pkg/bin/mtix", UID: 501, Protocol: 3})
	require.NoError(t, err)
	text := string(data)
	assert.True(t, strings.HasPrefix(text, "#!/bin/sh\n"))
	assert.Contains(t, text, "mtix_shim_target='/opt/pkg/bin/mtix'")
	assert.Contains(t, text, "mtix_shim_uid=501")
	assert.Contains(t, text, "attach protocol 3")
	assert.Contains(t, text, "PATH=/usr/bin:/bin")
	assert.Contains(t, text, "mtix doctor --fix")
	assert.NotContains(t, text, "mtix setup")
}

func makeLink(t *testing.T, dir, name, to string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.Symlink(to, path))
	return path
}

// plantedRepo is a working directory holding an executable ./attach, as a
// hostile repository could; the shim must never run it.
func plantedRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "attach"), []byte("echo PWNED-from-repo-cwd\n"), 0o700)) //nolint:gosec // fixture script
	return dir
}

func TestShim_LinkChainThroughWritableDirectory_Refused(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f shimFixture) string
	}{
		{"recorded path in a world-writable directory linking to /bin/sh", func(t *testing.T, f shimFixture) string {
			ww := filepath.Join(f.dir, "ww")
			require.NoError(t, os.Mkdir(ww, 0o700))
			require.NoError(t, os.Chmod(ww, 0o777))
			return makeLink(t, ww, "mtix", "/bin/sh")
		}},
		{"intermediate hop in a group-writable directory", func(t *testing.T, f shimFixture) string {
			gw := filepath.Join(f.dir, "gw")
			require.NoError(t, os.Mkdir(gw, 0o700))
			require.NoError(t, os.Chmod(gw, 0o770))
			hop := makeLink(t, gw, "hop", "/bin/sh")
			return makeLink(t, f.dir, "entry", hop)
		}},
		{"sticky directory not owned by root", func(t *testing.T, f shimFixture) string {
			st := filepath.Join(f.dir, "sticky")
			require.NoError(t, os.Mkdir(st, 0o700))
			require.NoError(t, os.Chmod(st, 0o777|os.ModeSticky))
			return makeLink(t, st, "mtix", f.target)
		}},
		{"writable ancestor of the final file", func(t *testing.T, f shimFixture) string {
			require.NoError(t, os.Chmod(f.dir, 0o777))
			return f.target
		}},
		{"directory link in a world-writable directory", func(t *testing.T, f shimFixture) string {
			ww := filepath.Join(f.dir, "W")
			require.NoError(t, os.Mkdir(ww, 0o700))
			require.NoError(t, os.Chmod(ww, 0o777))
			makeLink(t, ww, "x", filepath.Dir(f.target))
			return filepath.Join(ww, "x", "mtix")
		}},
		{"dot-dot after a directory link resolves physically", func(t *testing.T, f shimFixture) string {
			p, q := filepath.Join(f.dir, "P"), filepath.Join(f.dir, "Q")
			require.NoError(t, os.MkdirAll(filepath.Join(q, "realbin"), 0o700))
			require.NoError(t, os.Mkdir(p, 0o700))
			lib := filepath.Join(q, "lib")
			require.NoError(t, os.Mkdir(lib, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(lib, "mtix"), []byte("#!/bin/sh\necho PWNED-from-lib\n"), 0o700)) //nolint:gosec // fixture script
			require.NoError(t, os.Chmod(lib, 0o777))
			makeLink(t, p, "bin", filepath.Join(q, "realbin"))
			makeLink(t, filepath.Join(q, "realbin"), "mtix", "../lib/mtix")
			return filepath.Join(p, "bin", "mtix")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if os.Getuid() == 0 {
				t.Skip("root owns every fixture directory")
			}
			f := newShimFixture(t)
			t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) }) //nolint:errcheck // best-effort fixture cleanup
			f.cwd = plantedRepo(t)
			target := tt.setup(t, f)
			stdout, stderr, exit, goErr := f.run(t, target, os.Getuid(), "attach", "session-start")
			assert.Equal(t, 126, exit, stderr)
			assert.NotContains(t, stdout+stderr, "PWNED")
			assert.Contains(t, stderr, "writable by group or others")
			require.ErrorIs(t, goErr, ErrShimTargetRefused)
			assert.Contains(t, goErr.Error(), "writable by group or others")
		})
	}
}

func TestShim_HomebrewStyleRelativeChain_Accepted(t *testing.T) {
	f := newShimFixture(t)
	cellar := filepath.Join(f.dir, "opt", "homebrew", "Cellar", "mtix", "0.6.0", "bin")
	require.NoError(t, os.MkdirAll(cellar, 0o755))
	require.NoError(t, os.Rename(f.target, filepath.Join(cellar, "mtix")))
	bin := filepath.Join(f.dir, "opt", "homebrew", "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	link := makeLink(t, bin, "mtix", "../Cellar/mtix/0.6.0/bin/mtix")
	f.cwd = plantedRepo(t)
	stdout, stderr, exit, goErr := f.run(t, link, os.Getuid(), "inbox")
	assert.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, "ran:inbox")
	assert.NoError(t, goErr)
}

func TestShim_RootOwnedStickyDirectory_Accepted(t *testing.T) {
	info, err := os.Stat("/tmp")
	if err != nil || info.Mode()&os.ModeSticky == 0 || ownerOf(info) != 0 {
		t.Skip("this host has no root-owned sticky /tmp")
	}
	f := newShimFixture(t)
	link := filepath.Join("/tmp", uniqueTmpName("mtix-shim-test", f))
	require.NoError(t, os.Symlink(f.target, link))
	t.Cleanup(func() { _ = os.Remove(link) }) //nolint:errcheck // best-effort fixture cleanup
	stdout, stderr, exit, goErr := f.run(t, link, os.Getuid(), "inbox")
	assert.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, "ran:inbox")
	assert.NoError(t, goErr)
}
