// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package attach

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// ValidateShimTarget applies the shim's rules in Go, for doctor and the host
// dispatcher's re-resolve (ADR-007 §6.4-§6.5). Like the shim, it walks target
// one component at a time from /, following at most 32 symbolic links (file
// or directory links) and resolving .. physically. Every directory entered,
// and therefore every ancestor and the directory holding every link, must be
// owned by uid or root and not writable by group or others unless it is a
// root-owned sticky directory. Every link must be owned by uid or root. The
// final file must be a regular executable owned by uid or root and not
// writable by group or others. It returns the physical path, which is the
// path the shim executes; a refusal is a *ShimRefusal naming the refused
// component and wraps ErrShimTargetRefused.
func ValidateShimTarget(target string, uid int) (string, error) {
	if !strings.HasPrefix(target, "/") {
		return "", refuseAt(RefusedOther, target, "%s is not an absolute path", target)
	}
	t, err := resolveShimTarget(target, uid)
	if err != nil {
		return t, err
	}
	info, err := os.Lstat(t)
	if err != nil || !info.Mode().IsRegular() {
		return t, refuseAt(RefusedOther, t, "%s is not a regular file", t)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return t, refuseAt(RefusedNotExecutable, t, "%s is not executable", t)
	}
	if err := checkShimOwner(t, info, uid); err != nil {
		return t, err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return t, refuseAt(RefusedWritable, t, "%s is writable by group or others", t)
	}
	return t, nil
}

// shimWalk is the state of one component-by-component resolution. cur is
// the physical directory reached so far ("" is /) and is always checked.
type shimWalk struct {
	uid              int
	cur, rest, final string
	links            int
}

// resolveShimTarget walks target component by component and returns the
// physical path of its final component.
func resolveShimTarget(target string, uid int) (string, error) {
	if err := checkShimDir("/", uid); err != nil {
		return "", err
	}
	w := &shimWalk{uid: uid, rest: target}
	for w.rest != "" {
		if err := w.step(); err != nil {
			return "", err
		}
	}
	if w.final == "" {
		return "", refuseAt(RefusedOther, target, "%s does not name a file", target)
	}
	return w.final, nil
}

// step consumes one component: . is skipped, .. moves to the physical
// parent, a link is checked and spliced in, a directory is checked and
// entered, and the last component becomes the final file.
func (w *shimWalk) step() error {
	var c string
	c, w.rest = nextComponent(w.rest)
	switch c {
	case "", ".":
		return nil
	case "..":
		w.cur = parentOf(w.cur)
		return nil
	}
	p := w.cur + "/" + c
	info, err := os.Lstat(p)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return w.follow(p, info)
	case strings.Trim(w.rest, "/") == "":
		w.final, w.rest = p, ""
		return nil
	case err != nil || !info.IsDir():
		return refuseAt(RefusedOther, p, "%s is not a directory", p)
	}
	if dirErr := checkShimDir(p, w.uid); dirErr != nil {
		return dirErr
	}
	w.cur = p
	return nil
}

// follow checks link p (bound and owner) and splices its target into the
// remaining path; an absolute target restarts from /.
func (w *shimWalk) follow(p string, info os.FileInfo) error {
	if w.links++; w.links > maxShimLinks {
		return refuseAt(RefusedOther, p, "more than %d symbolic links", maxShimLinks)
	}
	if err := checkShimOwner(p, info, w.uid); err != nil {
		return err
	}
	link, err := os.Readlink(p)
	if err != nil || link == "" {
		return refuseAt(RefusedOther, p, "cannot read link %s", p)
	}
	if strings.HasPrefix(link, "/") {
		w.cur = ""
	}
	w.rest = link + "/" + w.rest
	return nil
}

// nextComponent splits the first path component off rest.
func nextComponent(rest string) (string, string) {
	rest = strings.TrimLeft(rest, "/")
	c, after, _ := strings.Cut(rest, "/")
	return c, after
}

// parentOf returns the parent of a physical directory ("" is /).
func parentOf(cur string) string {
	if i := strings.LastIndex(cur, "/"); i > 0 {
		return cur[:i]
	}
	return ""
}

// checkShimDir checks one physical directory: owned by uid or root and not
// writable by group or others, unless it is a root-owned sticky directory.
func checkShimDir(p string, uid int) error {
	info, err := os.Lstat(p)
	if err != nil {
		return refuseAt(RefusedOther, p, "cannot inspect %s", p)
	}
	if err := checkShimOwner(p, info, uid); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 && (info.Mode()&os.ModeSticky == 0 || ownerOf(info) != 0) {
		return refuseAt(RefusedWritable, p, "%s is writable by group or others", p)
	}
	return nil
}

func checkShimOwner(p string, info os.FileInfo, uid int) error {
	owner := ownerOf(info)
	if owner < 0 {
		return refuseAt(RefusedOther, p, "cannot inspect %s", p)
	}
	if owner != int64(uid) && owner != 0 {
		return refuseAt(RefusedOwner, p, "%s is owned by uid %d, neither the operator nor root", p, owner)
	}
	return nil
}

func ownerOf(info os.FileInfo) int64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int64(stat.Uid)
}

func refuseAt(reason RefusalReason, p, format string, args ...any) error {
	return &ShimRefusal{Path: p, Reason: reason, Detail: fmt.Sprintf(format, args...)}
}
