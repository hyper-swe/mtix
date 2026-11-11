// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// backupDestinationDocs are the texts that must carry the backup destination
// paragraph: the admin skill template, both plugin mirrors and the user manual.
var backupDestinationDocs = []string{
	"internal/docs/templates/skills/admin.md.tmpl",
	".claude-plugin/skills/mtix-admin.md",
	".codex-plugin/skills/admin/SKILL.md",
	"USERMANUAL.md",
}

// fileDestinationRefusedRE matches the statement that a destination starting
// with file: is refused.
var fileDestinationRefusedRE = regexp.MustCompile("(?is)destination that starts with `file:` is refused")

// recoverNotForRefusalRE matches a statement not to run mtix recover.
var recoverNotForRefusalRE = regexp.MustCompile("(?is)(?:do not run `mtix recover`|`mtix recover` does not apply)")

// runRecoverRE matches any instruction to run mtix recover, with the word
// before it so a negated form can be told apart.
var runRecoverRE = regexp.MustCompile("(?is)(\\w*)\\s+run `mtix recover`")

// TestBackupDestinationDocs_FileRefusalParagraph pins the backup destination
// paragraph in the admin skill, its plugin mirrors and the user manual: a
// file: destination is refused, ./file:name writes a literal file: path, and a
// destination refusal is not a reason to run mtix recover.
func TestBackupDestinationDocs_FileRefusalParagraph(t *testing.T) {
	set := loadShippedDocs(t)
	for _, path := range backupDestinationDocs {
		text, ok := set.all[path]
		require.True(t, ok, "%s is not in the shipped doc set", path)

		var hits []paragraph
		for _, p := range paragraphs(text) {
			if strings.Contains(p.text, "`mtix backup`") && fileDestinationRefusedRE.MatchString(p.text) {
				hits = append(hits, p)
			}
		}
		require.Len(t, hits, 1, "%s: want one backup paragraph saying a file: destination is refused", path)
		p := hits[0]
		where := path + ":" + itoa(p.line)

		require.Contains(t, p.text, "`./file:name`", "%s: backup paragraph does not name ./file:name for a literal file: path", where)
		require.Regexp(t, recoverNotForRefusalRE, p.text, "%s: backup paragraph does not say not to run mtix recover for a destination refusal", where)
		for _, m := range runRecoverRE.FindAllStringSubmatch(p.text, -1) {
			require.Equal(t, "not", strings.ToLower(m[1]), "%s: backup paragraph tells the reader to run mtix recover for a destination refusal", where)
		}
	}
}
