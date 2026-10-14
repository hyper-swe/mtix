// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func projectGit(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// projectExclusionBlock includes its separator so removal preserves user bytes.
const projectExclusionBlock = "\n# mtix: managed local briefing\n/MTIX.md\n"

// excludeLocalBriefing reconciles owned exclusion from all existing pointer files.
func excludeLocalBriefing(root string, pointers []string) (string, error) {
	path, pathErr := projectExcludePath(root)
	if pathErr != nil {
		return "", fmt.Errorf("reconcile project exclusion: %w", pathErr)
	}
	if path == "" {
		return "", nil
	}
	excluded, tracked := projectPointerPolicy(root, pointers)
	if updateErr := updateProjectExclusion(path, excluded); updateErr != nil {
		return "", fmt.Errorf("reconcile project exclusion: %w", updateErr)
	}
	if tracked {
		if _, ignoredErr := projectGit(root, "check-ignore", "-q", "--", "MTIX.md"); ignoredErr == nil {
			return "MTIX.md remains ignored by user Git rules; remove the user rule if you want to track it.", nil
		}
	}
	return "", nil
}

func projectExcludePath(root string) (string, error) {
	top, gitErr := projectGit(root, "rev-parse", "--show-toplevel")
	if gitErr != nil {
		return "", nil
	} // No usable repository means no exclusion writes.
	absRoot, absoluteErr := filepath.Abs(root)
	if absoluteErr != nil {
		return "", fmt.Errorf("resolve project root: %w", absoluteErr)
	}
	canonical, canonicalErr := filepath.EvalSymlinks(absRoot)
	if canonicalErr != nil {
		return "", fmt.Errorf("resolve project root links: %w", canonicalErr)
	}
	if filepath.Clean(top) != canonical {
		return "", nil
	}
	path, pathErr := projectGit(root, "rev-parse", "--git-path", "info/exclude")
	if pathErr != nil {
		return "", fmt.Errorf("locate Git local exclusion: %w", pathErr)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return path, nil
}

func projectPointerPolicy(root string, pointers []string) (bool, bool) {
	excluded, tracked := len(pointers) > 0, false
	for _, name := range pointers {
		indexed, indexErr := projectGit(root, "ls-files", "--error-unmatch", "--", name)
		if indexErr == nil && indexed != "" {
			excluded = false
			tracked = true
			continue
		}
		if _, ignoredErr := projectGit(root, "check-ignore", "-q", "--", name); ignoredErr != nil {
			excluded = false
		}
	}
	return excluded, tracked
}

func updateProjectExclusion(path string, excluded bool) error {
	content, _, readErr := readRegularDoc(path)
	if readErr != nil {
		return fmt.Errorf("read Git local exclusion: %w", readErr)
	}
	block := []byte(projectExclusionBlock)
	updated := content
	if excluded {
		if !bytes.Contains(content, block) {
			updated = append(append([]byte(nil), content...), block...)
		}
	} else {
		updated = bytes.ReplaceAll(content, block, nil)
	}
	if bytes.Equal(content, updated) {
		return nil
	}
	if writeErr := writeRegularDoc(path, updated); writeErr != nil {
		return fmt.Errorf("update Git local exclusion: %w", writeErr)
	}
	return nil
}
