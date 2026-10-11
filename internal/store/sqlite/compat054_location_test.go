// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Location helpers identify the released reader without requiring repository placement.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const compat054TagCommit = "5e9b641b36f37e5deb5a99fbee196d0d8a8ba2b1"

func compat054CachedBinary(cache, platform string) (string, error) {
	name := "mtix"
	if strings.HasPrefix(platform, "windows-") {
		name += ".exe"
	}
	path := filepath.Join(cache, "mtix-test", "mtix-v0.5.4-beta", compat054TagCommit, platform, name)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat real v0.5.4 reader cache: %w", err)
	}
	if !info.Mode().IsRegular() || (!strings.HasPrefix(platform, "windows-") && info.Mode().Perm()&0111 == 0) {
		return "", errors.New("real v0.5.4 reader cache is not executable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read real v0.5.4 reader cache: %w", err)
	}
	sum, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return "", fmt.Errorf("read real v0.5.4 reader checksum: %w", err)
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != strings.TrimSpace(string(sum)) {
		return "", errors.New("real v0.5.4 reader cache checksum mismatch")
	}
	return path, nil
}

func compat054FindRepository(start string) (string, error) {
	for dir := start; ; dir = filepath.Dir(dir) {
		metadata := filepath.Join(dir, ".git")
		info, err := os.Stat(metadata)
		if err == nil {
			if info.IsDir() {
				return dir, nil
			}
			raw, err := os.ReadFile(metadata)
			if err != nil {
				return "", fmt.Errorf("read tag repository metadata: %w", err)
			}
			if target, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: "); ok {
				if !filepath.IsAbs(target) {
					target = filepath.Join(dir, target)
				}
				if info, err := os.Stat(target); err == nil && info.IsDir() {
					return dir, nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read tag repository: %w", err)
		}
		if filepath.Dir(dir) == dir {
			return "", errors.New("real compatibility tag repository not found")
		}
	}
}

func compat054ReaderLocation(start, cache, platform string) (string, string, error) {
	cached, cacheErr := compat054CachedBinary(cache, platform)
	if cacheErr == nil {
		return cached, "", nil
	}
	repo, err := compat054FindRepository(start)
	if err != nil {
		return "", "", fmt.Errorf("real reader cache missing or invalid (%v); tag repository unavailable: %w", cacheErr, err)
	}
	return "", repo, nil
}
