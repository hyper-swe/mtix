// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	destructiveFlagRE = regexp.MustCompile(`--mode[ =]replace|--discard-local|\*\*replace\*\*|(?i:replace import)`)
	typedConfirmRE    = regexp.MustCompile(`(?i)typed|terminal|unattended`)
)

// TestDestructiveCommandDocs_QualifyReplaceAndDiscardLocal fails if any line
// of the agent instruction set (generated templates, .claude-plugin,
// .codex-plugin) that names `--mode replace`, `--discard-local`, the **replace** mode or a "replace import" fails to
// say the command needs the typed ticket count at a terminal or cannot run
// unattended (MTIX-107.74, MTIX-95.11.3). Both commands are gated by a confirmation no flag
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
			require.Truef(t, typedConfirmRE.MatchString(line),
				"%s:%d names a gated command without the typed confirmation: %s",
				path, i+1, strings.TrimSpace(line))
		}
	}
	require.GreaterOrEqual(t, checked, 20, "the scan found too few mentions; the file walk is broken")
}

// discardLocalGuardTerms are the parts of the guard every paragraph of the
// agent instruction set that names `--discard-local` states (MTIX-95.11.3):
// what it deletes, the push first, and the pending count to check.
var discardLocalGuardTerms = []string{"deletes", "mtix sync push", "pending"}

// TestDestructiveCommandDocs_DiscardLocalParagraphsCarryTheGuard fails if a
// paragraph of the agent instruction set that names `--discard-local` omits
// that it deletes local tasks and unpushed changes, the push first or the
// pending count. A paragraph that only warns against the command is exempt
// when it says "do not use".
func TestDestructiveCommandDocs_DiscardLocalParagraphsCarryTheGuard(t *testing.T) {
	set := loadShippedDocs(t)

	checked := 0
	for path, text := range set.agent {
		for _, p := range paragraphs(text) {
			if !strings.Contains(p.text, "--discard-local") || strings.Contains(p.text, "do not use") {
				continue
			}
			checked++
			for _, term := range discardLocalGuardTerms {
				assert.Containsf(t, p.text, term,
					"%s:%d names --discard-local without %q", path, p.line, term)
			}
		}
	}
	require.GreaterOrEqual(t, checked, 8, "the scan found too few paragraphs; the walk is broken")
}

// TestRecoverDocs_ForeignRepairedEvent_DocumentedInSkillAndManual fails if the
// recovery for a quarantined event from a teammate whose hub row was
// repaired is missing from the USERMANUAL, the admin skill template or a
// mirror (MTIX-95.11.3): the event stays quarantined, the recovery is the
// guarded rebuild, and `sync.max_lamport_jump` is never raised for a repaired
// row.
func TestRecoverDocs_ForeignRepairedEvent_DocumentedInSkillAndManual(t *testing.T) {
	set := loadShippedDocs(t)
	for _, path := range []string{
		"USERMANUAL.md",
		"internal/docs/templates/skills/admin.md.tmpl",
		".claude-plugin/skills/mtix-admin.md",
		".codex-plugin/skills/admin/SKILL.md",
	} {
		text, ok := set.all[path]
		require.Truef(t, ok, "%s is not in the shipped doc set", path)
		flat := strings.ToLower(strings.Join(strings.Fields(text), " "))
		assert.Contains(t, flat, "from a teammate whose hub row was repaired", path)
		assert.Contains(t, flat, "stays quarantined", path)
		assert.Contains(t, flat, "never raise `sync.max_lamport_jump` for a row the hub has repaired", path)
	}
}
