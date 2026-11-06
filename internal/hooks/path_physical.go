// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"errors"
	"os"
	"path/filepath"
)

// physicalPath is the one physical answer for a path an exec hook names
// (FR-19.2). Approval and launch both decide from it; no text comparison of
// the spelling is a decision.
type physicalPath struct {
	// Resolved is the path with every link followed the way the OS follows it.
	Resolved string
	// Rel is Resolved below the project root when Inside ("." for the root).
	Rel string
	// Inside is true when Resolved is the project root or lies under it, by
	// file identity.
	Inside bool
	// Linked is true when the path crosses a link that itself sits inside
	// the project: such a path is never pinned.
	Linked bool
	// Foreign is true when the path crosses a link from outside the project
	// that lands inside it anywhere but at the root itself.
	Foreign bool
	// Complete is false when no physical answer exists: the link budget ran
	// out, a link or file could not be read, or the path goes through a
	// per-process link that lands in the project. Callers refuse an
	// incomplete answer.
	Complete bool
}

// physicalLocation resolves path fully and classes it against the project
// root. A relative path is taken against root. Every failure to decide leaves
// Complete false (fail closed).
func physicalLocation(root, path string) physicalPath {
	if !filepath.IsAbs(path) {
		// Not Join: cleaning would apply a parent segment to link text.
		path = root + string(filepath.Separator) + path
	}
	phys := physicalPath{Complete: true}
	observe := func(link string) { phys.observeLink(root, link) }
	resolved, complete := resolveLinksObserved(path, 0, os.Readlink, observe)
	phys.Resolved = resolved
	if !complete {
		phys.Complete = false
		return phys
	}
	rel, inside, decided := rootRelative(root, resolved)
	phys.Rel, phys.Inside = rel, inside
	phys.Complete = phys.Complete && decided
	return phys
}

// observeLink classes one link met on the way: where the link sits, and where
// it lands.
func (p *physicalPath) observeLink(root, link string) {
	if _, inside, decided := rootRelative(root, filepath.Dir(link)); !decided {
		p.Complete = false
		return
	} else if inside {
		p.Linked = true
		return
	}
	target, complete := resolveDeepestStatus(link, 0)
	rel, inside, decided := rootRelative(root, target)
	switch {
	case !complete || !decided:
		p.Complete = false
	case unstableLink(link) && dependsOnProcessDirectory(link):
		// The launched hook runs from the project root, which may not be
		// this process's directory, so the route cannot be judged here.
		p.Complete = false
	case inside && unstableLink(link):
		// A per-process link can land elsewhere in the launched process
		// than here, so a route into the project cannot be pinned.
		p.Complete = false
	case inside && rel != ".":
		p.Foreign = true
	}
}

// rootRelative reports where path lies under root by file identity: some
// existing ancestor of path is the same file as root, and rel is the rest of
// the path below that ancestor ("." for the root itself). decided is false
// when identity could not be read; callers that must fail closed refuse an
// undecided answer (FR-19.2).
func rootRelative(root, path string) (rel string, inside, decided bool) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", false, false
	}
	var tail []string
	for current := filepath.Clean(path); ; {
		info, statErr := os.Stat(current)
		switch {
		case statErr == nil && os.SameFile(rootInfo, info):
			if len(tail) == 0 {
				return ".", true, true
			}
			return filepath.Join(tail...), true, true
		case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
			return "", false, false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, true
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
}

// dependsOnProcessDirectory reports whether a per-process link names a
// directory by absolute path (the working directory or root of whoever reads
// it). Such a link lands elsewhere in the launched hook than in this process.
// A link to another per-process directory by relative number, and links to
// files or descriptors, do not.
func dependsOnProcessDirectory(link string) bool {
	target, err := os.Readlink(link)
	if err != nil || !filepath.IsAbs(target) {
		return err != nil
	}
	info, err := os.Stat(link)
	return err != nil || info.IsDir()
}
