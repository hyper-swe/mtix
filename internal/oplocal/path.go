// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package oplocal provides validated operator state storage.
package oplocal

import (
	"fmt"
	"path"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// Env supplies paths from the operator process environment.
type Env struct {
	GOOS   string
	Home   string
	Values map[string]string
	root   string
}

// Dir resolves the operator configuration path on the requested platform.
func Dir(env Env) (string, error) {
	if env.GOOS == "windows" {
		base := env.Values["APPDATA"]
		if len(base) < 3 || base[1] != ':' || (base[2] != '\\' && base[2] != '/') {
			return "", invalid("APPDATA must be absolute")
		}
		if err := validatePlatformInput(env.GOOS, base); err != nil {
			return "", err
		}
		return strings.TrimRight(base, `\/`) + `\mtix`, nil
	}
	base := env.Values["XDG_CONFIG_HOME"]
	if base == "" {
		if !path.IsAbs(env.Home) {
			return "", invalid("home directory must be absolute")
		}
		if err := validatePlatformInput(env.GOOS, env.Home); err != nil {
			return "", err
		}
		base = path.Join(env.Home, ".config")
	}
	if !path.IsAbs(base) {
		return "", invalid("XDG_CONFIG_HOME must be absolute")
	}
	if err := validatePlatformInput(env.GOOS, base); err != nil {
		return "", err
	}
	return path.Join(base, "mtix"), nil
}

func invalid(reason string) error {
	return fmt.Errorf("operator state: %s: %w", reason, model.ErrOperatorStateUnreadable)
}

func inside(platform, target, root string) bool {
	if root == "" {
		return false
	}
	if platform == "windows" {
		target = strings.ReplaceAll(target, `\`, "/")
		root = strings.ReplaceAll(root, `\`, "/")
	}
	if platform == "windows" || platform == "darwin" {
		target = strings.ToLower(target)
		root = strings.ToLower(root)
	}
	target = path.Clean(target)
	root = strings.TrimRight(path.Clean(root), "/")
	return target == root || strings.HasPrefix(target, root+"/")
}

func validateLocation(env Env, target string) error {
	roots := excludedRoots(env)
	for _, root := range roots {
		if validatePlatformInput(env.GOOS, root) != nil {
			continue
		}
		if inside(env.GOOS, target, root) {
			return invalid("directory must be outside " + root)
		}
	}
	return nil
}

func validatePlatformInput(platform, input string) error {
	if platform == "windows" {
		input = strings.ReplaceAll(input, `\`, "/")
	}
	for _, part := range strings.Split(input, "/") {
		if part == ".." {
			return locationFailure(invalid("configured path contains a parent component"))
		}
	}
	return nil
}
