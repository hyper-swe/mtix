// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	destructiveFlagRE = regexp.MustCompile(`--mode[ =]replace|--discard-local|\*\*replace\*\*|(?i:replace import)`)
	humanOrTypedRE    = regexp.MustCompile(`(?i)typed|terminal|human`)
)

// TestDestructiveCommandDocs_QualifyReplaceAndDiscardLocal fails if any line
// of the agent instruction set (generated templates, .claude-plugin,
// .codex-plugin) that names `--mode replace`, `--discard-local`, the **replace** mode or a "replace import" fails to
// say the command needs the typed ticket count at a terminal or is the
// human's (MTIX-107.74). Both commands are gated by a confirmation no flag
// supplies (MTIX-90), so an unqualified mention reads as a command an agent
// may run.
//
// Scope: one physical line. A bullet, table row or code-block line is a line;
// a sentence wrapped over several lines must carry the qualifier on the line
// that names the command.
func TestDestructiveCommandDocs_QualifyReplaceAndDiscardLocal(t *testing.T) {
	set := loadShippedDocs(t)

	checked := 0
	for path, text := range set.agent {
		for i, line := range strings.Split(text, "\n") {
			if !destructiveFlagRE.MatchString(line) {
				continue
			}
			checked++
			require.Truef(t, humanOrTypedRE.MatchString(line),
				"%s:%d names a gated command without the typed confirmation or the human: %s",
				path, i+1, strings.TrimSpace(line))
		}
	}
	require.GreaterOrEqual(t, checked, 20, "the scan found too few mentions; the file walk is broken")
}
