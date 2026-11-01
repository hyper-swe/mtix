// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
)

// maxLinkExpansions bounds how many links one resolution follows, so a cycle
// ends (the OS refuses such a path, so nothing runs through it).
const maxLinkExpansions = 64

// resolveDeepest resolves path the way the OS does: component by component,
// following each link before the next component is applied, so a parent-
// directory segment after a link goes to the link target's parent, not to the
// parent of the link's text. Components that do not exist yet are attached
// lexically, so a path is judged where it would land; a dangling link is
// followed by its target. depth is the number of links already followed.
func resolveDeepest(path string, depth int) string {
	resolved, _ := resolveDeepestStatus(path, depth)
	return resolved
}

// resolveDeepestStatus reports whether resolution finished within its link budget.
func resolveDeepestStatus(path string, depth int) (string, bool) {
	return resolveLinks(path, depth, os.Readlink)
}

// resolveLinks is resolveDeepestStatus with the link reader supplied, so a
// read that fails can be exercised. A link that cannot be read is not
// resolved: the result is incomplete and callers refuse it.
func resolveLinks(path string, depth int, readlink func(string) (string, error)) (string, bool) {
	if !filepath.IsAbs(path) {
		return filepath.Clean(path), true
	}
	vol := filepath.VolumeName(path)
	root := vol + string(filepath.Separator)
	todo := splitPathComponents(path[len(vol):])
	cur, links := root, depth
	for len(todo) > 0 {
		comp := todo[0]
		todo = todo[1:]
		switch comp {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, comp)
		info, err := os.Lstat(next)
		if err != nil {
			return finishResolved(cur, append([]string{comp}, todo...)), true
		}
		if info.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		links++
		if links > maxLinkExpansions {
			// Past the limit the path is not resolved: it keeps the link it
			// stopped at, so it never equals its own resolution and is never
			// taken for a physical directory.
			return filepath.Join(append([]string{next}, todo...)...), false
		}
		target, readErr := readlink(next)
		if readErr != nil {
			return finishResolved(next, todo), false
		}
		if filepath.IsAbs(target) {
			targetVol := filepath.VolumeName(target)
			cur = targetVol + string(filepath.Separator)
			target = target[len(targetVol):]
		}
		todo = append(splitPathComponents(target), todo...)
	}
	return finishResolved(cur, nil), true
}
