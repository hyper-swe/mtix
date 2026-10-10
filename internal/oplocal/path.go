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
		return strings.TrimRight(base, `\/`) + `\mtix`, nil
	}
	base := env.Values["XDG_CONFIG_HOME"]
	if base == "" {
		if !path.IsAbs(env.Home) {
			return "", invalid("home directory must be absolute")
		}
		base = path.Join(env.Home, ".config")
	}
	if !path.IsAbs(base) {
		return "", invalid("XDG_CONFIG_HOME must be absolute")
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
		target = strings.ToLower(strings.ReplaceAll(target, `\`, "/"))
		root = strings.ToLower(strings.ReplaceAll(root, `\`, "/"))
	}
	target = path.Clean(target)
	root = strings.TrimRight(path.Clean(root), "/")
	return target == root || strings.HasPrefix(target, root+"/")
}

func validateLocation(env Env, target string) error {
	roots := excludedRoots(env)
	for _, root := range roots {
		if inside(env.GOOS, target, root) {
			return invalid("directory must be outside " + root)
		}
	}
	return nil
}
