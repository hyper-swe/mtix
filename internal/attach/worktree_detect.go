// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CheckoutKind says how a directory relates to a git checkout (MTIX-112.19.8,
// ADR-007 A5).
type CheckoutKind int

const (
	// CheckoutNone is no .git entry anywhere above the directory.
	CheckoutNone CheckoutKind = iota
	// CheckoutInvalid is a .git entry that is not provably a plain checkout or
	// a linked worktree (a forged commondir, a missing back-link, a symlink).
	CheckoutInvalid
	// CheckoutPlain is a checkout whose .git is a directory.
	CheckoutPlain
	// CheckoutWorktree is a linked worktree: a .git file naming an admin
	// directory whose commondir and back-link both agree.
	CheckoutWorktree
)

// Checkout is the result of DetectCheckout.
type Checkout struct {
	Kind CheckoutKind
	// Root is the directory holding .git.
	Root string
	// GitDir is the directory holding this checkout's index: Root/.git for a
	// plain checkout, the admin directory for a linked worktree.
	GitDir string
	// CommonRoot is the main checkout of a linked worktree.
	CommonRoot string
}

const (
	maxGitFileBytes = 4096
	maxWalkDepth    = 64
)

// DetectCheckout finds the checkout containing dir by reading .git entries
// only; it never runs git. A linked worktree is accepted only when the .git
// file names an admin directory under <common>/worktrees, that directory's
// commondir names <common>, and its gitdir back-link names this .git file. A
// .git file that points straight at a common dir, or a commondir without the
// back-link, is not a worktree and yields CheckoutInvalid (MTIX-112.19.8).
func DetectCheckout(dir string) (Checkout, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Checkout{}, fmt.Errorf("resolve %s: %w", dir, err)
	}
	// Listed paths are physical (registration resolves symlinks), so walk the
	// physical directory; an unresolvable one fails closed.
	cur, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Checkout{Kind: CheckoutInvalid}, nil //nolint:nilerr // unresolvable directory fails closed as an invalid checkout
	}
	for depth := 0; depth < maxWalkDepth; depth++ {
		info, statErr := os.Lstat(filepath.Join(cur, ".git"))
		switch {
		case statErr == nil && info.IsDir():
			return Checkout{Kind: CheckoutPlain, Root: cur, GitDir: filepath.Join(cur, ".git")}, nil
		case statErr == nil && info.Mode().IsRegular():
			return detectLinkedWorktree(cur), nil
		case statErr == nil, !errors.Is(statErr, os.ErrNotExist):
			return Checkout{Kind: CheckoutInvalid}, nil // a symlink, FIFO or unreadable entry: fail closed
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return Checkout{}, nil
}

// detectLinkedWorktree validates the .git file of root as a linked worktree.
func detectLinkedWorktree(root string) Checkout {
	none := Checkout{Kind: CheckoutInvalid}
	gitFile := filepath.Join(root, ".git")
	body, ok := readSmallFile(gitFile)
	if !ok || !strings.HasPrefix(body, "gitdir:") {
		return none
	}
	admin := strings.TrimSpace(strings.TrimPrefix(body, "gitdir:"))
	if admin == "" {
		return none
	}
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(root, admin)
	}
	admin, err := filepath.EvalSymlinks(filepath.Clean(admin))
	if err != nil {
		return none
	}
	commonRaw, ok := readSmallFile(filepath.Join(admin, "commondir"))
	if !ok {
		return none
	}
	common := strings.TrimSpace(commonRaw)
	if !filepath.IsAbs(common) {
		common = filepath.Join(admin, common)
	}
	backRaw, ok := readSmallFile(filepath.Join(admin, "gitdir"))
	back := strings.TrimSpace(backRaw)
	if !filepath.IsAbs(back) { // git worktree add --relative-paths writes a link relative to the admin dir
		back = filepath.Join(admin, back)
	}
	if !ok || !sameFile(back, gitFile) {
		return none
	}
	if !sameFile(filepath.Dir(filepath.Dir(admin)), common) || filepath.Base(filepath.Dir(admin)) != "worktrees" {
		return none
	}
	physicalCommon, err := filepath.EvalSymlinks(common)
	if err != nil || filepath.Base(physicalCommon) != ".git" {
		return none // a bare common dir has no main checkout to attach to
	}
	return Checkout{Kind: CheckoutWorktree, Root: root, GitDir: admin, CommonRoot: filepath.Dir(physicalCommon)}
}

// readSmallFile reads a regular file of at most maxGitFileBytes; anything else
// reports false so callers fail closed.
func readSmallFile(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxGitFileBytes {
		return "", false
	}
	f, err := os.Open(path) // #nosec G304 -- path is derived from a .git entry and size-checked above
	if err != nil {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxGitFileBytes))
	if closeErr := f.Close(); err != nil || closeErr != nil {
		return "", false
	}
	return string(raw), true
}

// sameFile compares two paths after resolving symlinks; a path that cannot be
// resolved never matches, so a missing path never matches by accident.
func sameFile(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	return ra == rb
}
