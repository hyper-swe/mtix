// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

func logExcludedRoot(root string, err error) {
	slog.Debug("configured exclusion is unavailable", "root", root, "error", err)
}

func canonicalExclusion(input string, resolve func(string) (string, error)) (string, error) {
	if input == "" || !utf8.ValidString(input) || strings.ContainsRune(input, 0) {
		return "", invalid("configured exclusion is invalid")
	}
	if !filepath.IsAbs(input) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		input = cwd + string(filepath.Separator) + input
	}
	prefix, suffix, err := existingExclusionPrefix(input)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(prefix)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", invalid("configured exclusion type is invalid")
	}
	canonical, err := resolve(prefix)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		canonical = filepath.Join(canonical, suffix[i])
	}
	if !filepath.IsAbs(canonical) {
		return "", invalid("configured exclusion must be absolute")
	}
	return filepath.Clean(canonical), nil
}

func existingExclusionPrefix(input string) (string, []string, error) {
	current := input
	separators := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		separators = `\/`
	}
	var suffix []string
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return "", nil, invalid("configured exclusion type is invalid")
			}
			return current, suffix, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		end := strings.LastIndexAny(strings.TrimRight(current, separators), separators)
		if end < 0 {
			return "", nil, err
		}
		suffix = append(suffix, current[end+1:])
		next := current[:end]
		if next == filepath.VolumeName(current) {
			next += string(filepath.Separator)
		}
		if next == current {
			return "", nil, err
		}
		current = next
	}
}
