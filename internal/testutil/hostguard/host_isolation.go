// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package hostguard keeps test processes away from the developer's real user
// configuration. It depends only on the standard library so any package,
// including internal/oplocal, can use it from TestMain without an import cycle.
//
// The harness points HOME, USERPROFILE, XDG_CONFIG_HOME and APPDATA at a
// scratch tree and repoints TMPDIR at a sibling directory. The scratch tree is
// created under the ORIGINAL temporary directory, so oplocal's TMPDIR
// exclusion still accepts the home. Nothing is created under the real home,
// and the real configuration directories are never read or compared, because
// other live sessions on the same host write them.
package hostguard

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// RootEnv names the scratch root of the isolated process. Child processes
	// inherit it, so a re-executed test binary reuses the parent's tree.
	RootEnv = "MTIX_TEST_HOST_ROOT"
	// RealHomeEnv records the real home directory seen before isolation. Assert
	// refuses any isolated variable that equals it.
	RealHomeEnv = "MTIX_TEST_REAL_HOME"
	// OriginalTmpEnv records the temporary directory before TMPDIR was repointed.
	OriginalTmpEnv = "MTIX_TEST_ORIGINAL_TMPDIR"

	scratchPrefix = "mtix-host-"
)

// guardedVars are the variables that must resolve inside the scratch root.
var guardedVars = []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"}

// Layout describes one scratch tree.
type Layout struct {
	Root   string
	Home   string
	Config string
	Tmp    string
}

// sharedTemp reports whether dir is a world-writable system temp root, which
// oplocal refuses as an operator-state location.
func sharedTemp(dir string) bool {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	for _, root := range []string{"/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp"} {
		if dir == root || strings.HasPrefix(dir, root+"/") {
			return true
		}
	}
	return false
}

// ScratchBaseEnv lets a CI job name an acceptable scratch base explicitly. It
// is never defaulted: it must be absolute, outside the real home and outside
// the shared temp roots.
const ScratchBaseEnv = "MTIX_TEST_SCRATCH_BASE"

// chooseBase picks the scratch base. Candidates in order: the ORIGINAL temp
// directory, ScratchBaseEnv, XDG_RUNTIME_DIR. A candidate is refused when it is
// relative, inside a shared temp root (oplocal rejects any ancestor that is
// group or other writable, so /tmp and /var/tmp can never host an operator
// home) or inside the real home. There is no fallback to the real home.
func chooseBase(getenv func(string) string, tempDir, realHome string) (string, error) {
	candidates := []string{getenv(OriginalTmpEnv), tempDir, getenv(ScratchBaseEnv), getenv("XDG_RUNTIME_DIR")}
	for _, c := range candidates {
		if c == "" || !filepath.IsAbs(c) || sharedTemp(c) {
			continue
		}
		if realHome != "" && sameOrInside(realHome, c) {
			continue
		}
		return c, nil
	}
	// CI only: a GitHub-hosted runner is ephemeral, not a person's home, so its
	// RUNNER_TEMP is accepted even though it sits under the runner's home.
	if runner := getenv("RUNNER_TEMP"); getenv("GITHUB_ACTIONS") == "true" && filepath.IsAbs(runner) && !sharedTemp(runner) {
		return runner, nil
	}
	return "", fmt.Errorf("no acceptable scratch base: TMPDIR is a shared temp root or inside the real home. Set %s to a private 0700 directory outside the home and outside /tmp, for example: mkdir -m 700 /run/user/$(id -u)/mtix-test && export %s=/run/user/$(id -u)/mtix-test (/tmp and /var/tmp are refused by oplocal)", ScratchBaseEnv, ScratchBaseEnv)
}

func sameOrInside(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func baseDir() (string, error) {
	realHome := os.Getenv(RealHomeEnv)
	if realHome == "" {
		realHome, _ = os.UserHomeDir()
	}
	return chooseBase(os.Getenv, os.TempDir(), realHome)
}

// adoptExisting joins a scratch tree created by a parent process when this
// process was started with a reduced environment that kept only HOME.
func adoptExisting() bool {
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return false
	}
	root := filepath.Dir(home)
	if !strings.HasPrefix(filepath.Base(root), scratchPrefix) || filepath.Join(root, "home") != filepath.Clean(home) {
		return false
	}
	config := filepath.Join(home, "operator")
	for k, v := range map[string]string{RootEnv: root, "USERPROFILE": home, "XDG_CONFIG_HOME": config, "APPDATA": config} {
		if os.Getenv(k) == "" {
			if err := os.Setenv(k, v); err != nil {
				return false
			}
		}
	}
	return true
}

func newLayout(base string) (Layout, error) {
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		return Layout{}, fmt.Errorf("resolve scratch base %s: %w", base, err)
	}
	root, err := os.MkdirTemp(resolved, scratchPrefix)
	if err != nil {
		return Layout{}, fmt.Errorf("create scratch root: %w", err)
	}
	l := Layout{Root: root, Home: filepath.Join(root, "home"), Tmp: filepath.Join(root, "tmp")}
	l.Config = filepath.Join(l.Home, "operator")
	for _, dir := range []string{l.Home, l.Config, l.Tmp} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return Layout{}, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return l, nil
}

func (l Layout) values(base string) map[string]string {
	return map[string]string{
		"HOME": l.Home, "USERPROFILE": l.Home,
		"XDG_CONFIG_HOME": l.Config, "APPDATA": l.Config,
		"TMPDIR": l.Tmp, "TMP": l.Tmp, "TEMP": l.Tmp,
		RootEnv: l.Root, OriginalTmpEnv: base,
	}
}

// Run isolates the whole test process, asserts the isolation, runs the tests
// and removes the scratch tree. Call it from TestMain: os.Exit(hostguard.Run(m)).
func Run(m *testing.M) int {
	owner := os.Getenv(RootEnv) == "" && !adoptExisting()
	if owner {
		code, ok := setUp()
		if !ok {
			return code
		}
		defer tearDown()
		if err := Assert(); err != nil {
			fmt.Fprintln(os.Stderr, "host config isolation:", err)
			return 1
		}
	}
	// A re-executed helper inherits a tree its parent already asserted. It is
	// not asserted again, because a test may start it with a deliberately
	// malformed path variable to exercise config resolution.
	return m.Run()
}

func setUp() (int, bool) {
	base, err := baseDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "host config isolation:", err)
		return 1, false
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if setErr := os.Setenv(RealHomeEnv, home); setErr != nil {
			fmt.Fprintln(os.Stderr, "host config isolation:", setErr)
			return 1, false
		}
	}
	l, err := newLayout(base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "host config isolation:", err)
		return 1, false
	}
	for k, v := range l.values(base) {
		if setErr := os.Setenv(k, v); setErr != nil {
			fmt.Fprintln(os.Stderr, "host config isolation:", setErr)
			return 1, false
		}
	}
	return 0, true
}

func tearDown() {
	if err := removeTree(os.Getenv(RootEnv)); err != nil {
		fmt.Fprintln(os.Stderr, "host config isolation cleanup:", err)
	}
}

// Isolate gives one test its own scratch tree and returns its layout. The
// environment is restored and the tree removed when the test ends.
func Isolate(t testing.TB) Layout {
	t.Helper()
	base, err := baseDir()
	if err != nil {
		t.Fatalf("host config isolation: %v", err)
	}
	l, err := newLayout(base)
	if err != nil {
		t.Fatalf("host config isolation: %v", err)
	}
	t.Cleanup(func() {
		if err := removeTree(l.Root); err != nil {
			t.Errorf("host config isolation cleanup: %v", err)
		}
	})
	for k, v := range l.values(base) {
		t.Setenv(k, v)
	}
	return l
}

// Assert fails unless every guarded variable resolves inside the scratch root
// and none of them equals the recorded real home.
func Assert() error {
	root := os.Getenv(RootEnv)
	if root == "" {
		return errors.New("host config not isolated: " + RootEnv + " is unset; install hostguard.Run in TestMain")
	}
	for _, key := range guardedVars {
		value := os.Getenv(key)
		if value == "" {
			return fmt.Errorf("host config not isolated: %s is unset", key)
		}
		if realHome := os.Getenv(RealHomeEnv); realHome != "" && filepath.Clean(value) == filepath.Clean(realHome) {
			return fmt.Errorf("host config not isolated: %s is the real home", key)
		}
		if !inside(root, value) {
			return fmt.Errorf("host config not isolated: %s=%s is outside the scratch root", key, value)
		}
	}
	return nil
}

// AssertPath fails unless p lies inside the scratch root.
func AssertPath(p string) error {
	root := os.Getenv(RootEnv)
	if root == "" || !inside(root, p) {
		return fmt.Errorf("path %s is outside the isolated host root", p)
	}
	return nil
}

// ChildEnv asserts the isolation and returns env with the guarded variables
// forced to the isolated values, replacing any inherited real ones.
func ChildEnv(t testing.TB, env []string) []string {
	t.Helper()
	if err := Assert(); err != nil {
		t.Fatalf("refusing to start a child process: %v", err)
	}
	forced := append(append([]string{}, guardedVars...), RootEnv, OriginalTmpEnv, RealHomeEnv, "TMPDIR")
	out := make([]string, 0, len(env)+len(forced))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if !contains(forced, key) {
			out = append(out, kv)
		}
	}
	for _, key := range forced {
		if v, ok := os.LookupEnv(key); ok {
			out = append(out, key+"="+v)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func inside(root, p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// removeTree deletes a scratch root created by this package: files first, then
// directories bottom-up. It refuses any path that is not a scratch root.
func removeTree(root string) error {
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), scratchPrefix) {
		return fmt.Errorf("refuse to remove %q: not a scratch root", root)
	}
	scratch, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open scratch root: %w", err)
	}
	var files, dirs []string
	walkErr := fs.WalkDir(scratch.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		} else {
			files = append(files, path)
		}
		return nil
	})
	if walkErr == nil {
		walkErr = removeAll(scratch, files, dirs)
	}
	closeErr := scratch.Close()
	if walkErr != nil {
		return fmt.Errorf("remove scratch root: %w", walkErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close scratch root: %w", closeErr)
	}
	return removeRootDir(root)
}

// removeRootDir removes the now-empty scratch root through its parent handle.
func removeRootDir(root string) error {
	parent, err := os.OpenRoot(filepath.Dir(root))
	if err != nil {
		return fmt.Errorf("open scratch parent: %w", err)
	}
	rmErr := parent.Remove(filepath.Base(root))
	if closeErr := parent.Close(); rmErr == nil && closeErr != nil {
		return fmt.Errorf("close scratch parent: %w", closeErr)
	}
	if rmErr != nil {
		return fmt.Errorf("remove %s: %w", root, rmErr)
	}
	return nil
}

// removeAll deletes files first, then directories bottom-up, through the
// root-scoped handle so a symlink cannot redirect a removal outside the tree.
func removeAll(scratch *os.Root, files, dirs []string) error {
	for _, f := range files {
		if err := scratch.Remove(f); err != nil {
			return err
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if dirs[i] == "." {
			continue
		}
		if err := scratch.Remove(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}
