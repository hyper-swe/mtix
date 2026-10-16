// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Harness marker policy is shared by confirmation logging and legacy hooks.
// Wildcards are confirmation-only; Legacy preserves existing hook refusals.
var harnessMarkerPolicy = [...]struct {
	Pattern string
	Legacy  bool
}{
	{"CLAUDECODE", true},
	{"CLAUDE_CODE_MESSAGING_SOCKET", true},
	{"CLAUDE_ENV_FILE", true},
	{"CODEX_THREAD_ID", true},
	{"CODEX_SANDBOX", true},
	{"CODEX_SANDBOX_NETWORK_DISABLED", true},
	{"PI_SESSION_ID", true},
	{"PI_SESSION_FILE", true},
	{"PI_CODING_AGENT", true},
	{"CLAUDE_CODE_*", false},
	{"CODEX_SANDBOX*", false},
	{"PI_*", false},
}

// LegacyHarnessSessionMarked keeps the original hook predicate on shared policy.
func LegacyHarnessSessionMarked(env []string) bool {
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		for _, rule := range harnessMarkerPolicy {
			if rule.Legacy && name == rule.Pattern {
				return true
			}
		}
	}
	return false
}

// RequireOperatorTTY requires a real terminal, independently of harness markers.
func RequireOperatorTTY(stdin *os.File) error {
	if !OperatorTerminal(stdin) {
		return fmt.Errorf("operator confirmation requires terminal stdin; use --yes for non-interactive confirmation")
	}
	return nil
}

// HarnessSessionMarkers records names only, never their potentially private values.
func HarnessSessionMarkers(env []string) []string {
	names := map[string]bool{}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		for _, rule := range harnessMarkerPolicy {
			pattern := rule.Pattern
			prefix, wildcard := strings.CutSuffix(pattern, "*")
			if name == pattern || (wildcard && strings.HasPrefix(name, prefix)) {
				names[name] = true
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
