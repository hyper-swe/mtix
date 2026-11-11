// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// envMessagingSocket names the variable that sets the messaging socket path
// a session registers; the driver points it inside the scratch root.
const envMessagingSocket = "CLAUDE_CODE_MESSAGING_SOCKET"

// launchFlags are the flag values CheckLaunch collects while walking args.
type launchFlags struct {
	settings       []string
	settingSources []string
	sourcesGiven   bool
	addDirs        []string
}

// collectFlags walks the claude arguments and records the settings files,
// the setting sources and the extra directories (--add-dir is variadic: it
// takes every following non-flag argument).
func collectFlags(args []string) launchFlags {
	var f launchFlags
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--settings" || strings.HasPrefix(a, "--settings="):
			var v string
			v, i = flagValue(args, i, "--settings")
			f.settings = append(f.settings, v)
		case a == "--setting-sources" || strings.HasPrefix(a, "--setting-sources="):
			var v string
			v, i = flagValue(args, i, "--setting-sources")
			f.sourcesGiven = true
			for _, s := range strings.Split(v, ",") {
				f.settingSources = append(f.settingSources, strings.ToLower(strings.TrimSpace(s)))
			}
		case a == "--add-dir" || strings.HasPrefix(a, "--add-dir="):
			if v, ok := strings.CutPrefix(a, "--add-dir="); ok {
				f.addDirs = append(f.addDirs, v)
				continue
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				f.addDirs = append(f.addDirs, args[i])
			}
		}
	}
	return f
}

// checkIsolation refuses extra directories outside the root and any plan
// that would load user-level Claude Code settings: a CLAUDE_CONFIG_DIR
// inside the root, or --setting-sources without "user", is required.
func checkIsolation(root string, f launchFlags, env []string) error {
	for _, d := range f.addDirs {
		if err := requireInside(root, d, "add-dir"); err != nil {
			return err
		}
	}
	hasConfigDir := false
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); k == "CLAUDE_CONFIG_DIR" {
			hasConfigDir = true // checkEnv already proved it is inside the root
		}
	}
	userLoaded := !f.sourcesGiven
	for _, s := range f.settingSources {
		if s == "user" {
			userLoaded = true
			break
		}
	}
	if userLoaded && !hasConfigDir {
		return fmt.Errorf("%w: user settings would load from outside the scratch root: set CLAUDE_CONFIG_DIR inside it or pass --setting-sources without user", ErrLaunchRefused)
	}
	return nil
}

// checkSettingContents refuses every settings file the session will read
// from the plan (each --settings file, and the project settings of the
// working directory) when it sets permissions.defaultMode to
// bypassPermissions. A missing file is fine; an unreadable or invalid one is
// refused because its content cannot be proven safe.
func checkSettingContents(cwd string, files []string) error {
	all := append([]string(nil), files...)
	for _, n := range []string{"settings.json", "settings.local.json"} {
		all = append(all, filepath.Join(cwd, ".claude", n))
	}
	for _, p := range all {
		if err := checkSettingFile(p); err != nil {
			return err
		}
	}
	return nil
}

func checkSettingFile(p string) error {
	data, err := os.ReadFile(p) //nolint:gosec // G304: the plan names the settings files to inspect
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read settings %s: %w", ErrLaunchRefused, p, err)
	}
	var doc struct {
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%w: settings %s is not valid JSON: %w", ErrLaunchRefused, p, err)
	}
	if strings.EqualFold(strings.TrimSpace(doc.Permissions.DefaultMode), "bypassPermissions") {
		return fmt.Errorf("%w: settings %s sets permissions.defaultMode to bypassPermissions", ErrLaunchRefused, p)
	}
	return nil
}
