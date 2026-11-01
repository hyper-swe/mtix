// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Lookup of the 0.5.x per-project unit file (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// unitFileLimit bounds how much of a candidate unit file is read.
const unitFileLimit = 64 * 1024

var (
	plistRootPattern   = regexp.MustCompile(`<string>-C</string>\s*<string>([^<]+)</string>`)
	systemdRootPattern = regexp.MustCompile(`(?m)^ExecStart=.* -C (.+) daemon --interval [0-9]+$`)
)

// legacyUnitInstalled reports whether the per-project unit for projectRoot exists on disk.
// The unit name hashes the root as `mtix daemon install` saw it, so a project installed through
// a symlink is not found under the canonical root's name; the lookup then reads the other
// per-project units and compares the project directory each one runs. A sibling it cannot read
// (a dangling link, a directory, a file it may not open) is skipped and returned in skipped for
// doctor to report: it never stops the answer for this board or any other. This board's own
// unit, named by its hash, counts as installed whenever it can be seen, readable or not.
// Platforms without a unit file (Task Scheduler) report false; doctor then relies on the PID file.
func legacyUnitInstalled(goos, projectRoot, home string) (installed bool, skipped []string, err error) {
	spec, err := buildServiceSpec(goos, "mtix", projectRoot, home, daemonDispatchDefaultIntervalSec)
	if err != nil || spec.Path == "" {
		return false, nil, err
	}
	if _, statErr := os.Lstat(spec.Path); !errors.Is(statErr, os.ErrNotExist) {
		return true, nil, nil // present, or not provably absent: the safe side
	}
	installed, skipped = unitRunsProject(spec.Path, projectSlug(projectRoot), projectRoot)
	return installed, skipped, nil
}

// unitRunsProject scans the sibling per-project units of namedPath (whose name embeds slug) for
// one whose command line serves projectRoot, comparing directories after symlink resolution.
// Entries it cannot read, and a unit directory it cannot list, are returned in skipped.
func unitRunsProject(namedPath, slug, projectRoot string) (found bool, skipped []string) {
	dir, base := filepath.Dir(namedPath), filepath.Base(namedPath)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(strings.TrimSuffix(base, ext), slug)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, []string{dir}
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		candidate := filepath.Join(dir, name)
		root, readErr := unitProjectRoot(candidate)
		if readErr != nil {
			skipped = append(skipped, candidate)
		} else if root != "" && sameDirectory(root, projectRoot) {
			found = true
		}
	}
	return found, skipped
}

// unitProjectRoot reads the -C project directory out of a launchd plist or systemd unit; ""
// means the file is not a per-project daemon unit.
func unitProjectRoot(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- a path listed under the user's own unit directory
	if err != nil {
		return "", fmt.Errorf("read legacy unit: %w", err)
	}
	buf := make([]byte, unitFileLimit)
	n, readErr := file.Read(buf)
	if closeErr := file.Close(); closeErr != nil {
		return "", fmt.Errorf("close legacy unit: %w", closeErr)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", fmt.Errorf("read legacy unit: %w", readErr)
	}
	for _, pattern := range []*regexp.Regexp{plistRootPattern, systemdRootPattern} {
		if m := pattern.FindSubmatch(buf[:n]); m != nil {
			return string(m[1]), nil
		}
	}
	return "", nil
}
