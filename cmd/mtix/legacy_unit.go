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
// per-project units and compares the project directory each one runs.
// Platforms without a unit file (Task Scheduler) report false; doctor then relies on the PID file.
func legacyUnitInstalled(goos, projectRoot, home string) (bool, error) {
	spec, err := buildServiceSpec(goos, "mtix", projectRoot, home, daemonDispatchDefaultIntervalSec)
	if err != nil || spec.Path == "" {
		return false, err
	}
	_, statErr := os.Lstat(spec.Path)
	if statErr == nil {
		return true, nil
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect legacy unit: %w", statErr)
	}
	return unitRunsProject(spec.Path, projectSlug(projectRoot), projectRoot)
}

// unitRunsProject scans the sibling per-project units of namedPath (whose name embeds slug) for
// one whose command line serves projectRoot, comparing directories after symlink resolution.
func unitRunsProject(namedPath, slug, projectRoot string) (bool, error) {
	base, ext := filepath.Base(namedPath), filepath.Ext(namedPath)
	prefix := strings.TrimSuffix(strings.TrimSuffix(base, ext), slug)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(namedPath), prefix+"*"+ext))
	if err != nil {
		return false, fmt.Errorf("list legacy units: %w", err)
	}
	for _, candidate := range matches {
		root, readErr := unitProjectRoot(candidate)
		if readErr != nil {
			return false, readErr
		}
		if root != "" && sameDirectory(root, projectRoot) {
			return true, nil
		}
	}
	return false, nil
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
