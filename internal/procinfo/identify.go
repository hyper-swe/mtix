// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo

import (
	"path"
	"strings"
)

// Harness names returned by Identify.
const (
	HarnessClaude = "claude-code"
	HarnessCodex  = "codex"
	HarnessPi     = "pi"
)

// normalizeBase lower-cases the last path element and drops a login dash.
func normalizeBase(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(path.Base(strings.ReplaceAll(s, `\`, "/")), "-"))
}

// Identify names the harness that owns a process from its kernel name, its
// argv[0] and its executable path, or returns "" when it is not a known
// harness (or is ambiguous). Claude Code installs `claude` as a symlink to
// versions/<version>, so its kernel name is a version string: argv[0] and the
// path under /claude/versions/ carry the identity. A match is by whole path
// element, never by substring of a directory such as "pi".
func Identify(p Proc) string {
	found := map[string]bool{}
	for _, s := range []string{p.Name, p.Argv0, p.Exe} {
		switch normalizeBase(s) {
		case "claude":
			found[HarnessClaude] = true
		case "codex":
			found[HarnessCodex] = true
		case "pi":
			found[HarnessPi] = true
		}
	}
	if strings.Contains(strings.ReplaceAll(p.Exe, `\`, "/"), "/claude/versions/") {
		found[HarnessClaude] = true
	}
	if len(found) != 1 {
		return ""
	}
	for h := range found {
		return h
	}
	return ""
}

// ParseProcArgs2 extracts the executable path and argv[0] from a
// KERN_PROCARGS2 buffer: an int32 argument count, the executable path, NUL
// padding, then the NUL-separated arguments.
func ParseProcArgs2(raw []byte) (exe, argv0 string, ok bool) {
	if len(raw) <= 4 {
		return "", "", false
	}
	rest := raw[4:]
	end := indexNUL(rest)
	if end < 0 {
		return "", "", false
	}
	exe, rest = string(rest[:end]), rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	end = indexNUL(rest)
	if end < 0 {
		return exe, "", false
	}
	return exe, string(rest[:end]), true
}

func indexNUL(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return -1
}
