// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// restoreCopyRE matches an instruction that restores a backup or snapshot by
// copying (copy, copied, copies, cp) or restoring it over the SQLite store file.
var restoreCopyRE = regexp.MustCompile(`(?is)(?:\bcop(?:y|ied|ies|ying)\b|\bcp\b|\brestor\w*).{0,100}?\b(?:over|onto)\b.{0,60}?mtix\.db\b`)

// stopWordRE finds "stop" or "stops" as a whole word.
var stopWordRE = regexp.MustCompile(`\bstops?\b`)

// TestRestoreInstructions_NameWalAndShmDeletion fails when a shipped
// instruction restores a backup by copying it over .mtix/data/mtix.db without
// also telling the reader to delete mtix.db-wal and mtix.db-shm (MTIX-95.36).
// A stale write-ahead log replays old writes onto the restored file.
//
// Scope: the paragraph (blank-line-separated block) holding the copy
// instruction must mention both files and must name stopping every mtix
// process, in the order stop, copy, delete.
func TestRestoreInstructions_NameWalAndShmDeletion(t *testing.T) {
	set := loadShippedDocs(t)

	found := 0
	for path, text := range set.all {
		for _, p := range paragraphs(text) {
			loc := restoreCopyRE.FindStringIndex(p.text)
			if loc == nil {
				continue
			}
			found++
			assertRestoreOrder(t, path, p, loc[1])
		}
	}
	// The scan must reach the known sites, or it proves nothing.
	require.GreaterOrEqual(t, found, 12, "restore-instruction scan found too few sites; the matcher or the file walk is broken")
}

// assertRestoreOrder checks one restore paragraph.
func assertRestoreOrder(t *testing.T, path string, p paragraph, copyAt int) {
	t.Helper()
	where := path + ":" + itoa(p.line)
	low := strings.ToLower(p.text)
	wal := strings.LastIndex(low, "mtix.db-wal")
	shm := strings.LastIndex(low, "mtix.db-shm")
	stop := -1
	if loc := stopWordRE.FindStringIndex(low); loc != nil {
		stop = loc[0]
	}
	require.GreaterOrEqual(t, wal, 0, "%s: restore by copy omits deleting mtix.db-wal", where)
	require.GreaterOrEqual(t, shm, 0, "%s: restore by copy omits deleting mtix.db-shm", where)
	require.GreaterOrEqual(t, stop, 0, "%s: restore by copy omits stopping every mtix process", where)
	require.Less(t, stop, copyAt, "%s: stop the processes before copying", where)
	require.Greater(t, wal, copyAt, "%s: delete mtix.db-wal after the copy", where)
	require.Greater(t, shm, copyAt, "%s: delete mtix.db-shm after the copy", where)
}

// TestRestoreInstructions_AdminSkillMirrorsMatch pins that the admin skill
// template and its .claude-plugin mirror carry identical restore paragraphs
// (MTIX-95.36).
func TestRestoreInstructions_AdminSkillMirrorsMatch(t *testing.T) {
	set := loadShippedDocs(t)
	tmpl := restoreParagraphs(set.agent["internal/docs/templates/skills/admin.md.tmpl"])
	mirror := restoreParagraphs(set.agent[".claude-plugin/skills/mtix-admin.md"])
	require.NotEmpty(t, tmpl, "admin skill template has no restore paragraph")
	require.Equal(t, tmpl, mirror, "admin skill template and plugin mirror restore steps differ")
}

// restoreParagraphs returns the sorted restore paragraphs of text.
func restoreParagraphs(text string) []string {
	var out []string
	for _, p := range paragraphs(text) {
		if restoreCopyRE.MatchString(p.text) {
			out = append(out, p.text)
		}
	}
	sort.Strings(out)
	return out
}
