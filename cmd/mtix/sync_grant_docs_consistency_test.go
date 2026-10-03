// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// grantLike matches any line that grants or revokes, at any indentation and
// in any case, so a statement that does not parse as an expected
// mtix_writer grant cannot hide in a documented sql block.
var grantLike = regexp.MustCompile(`(?i)^\s*(grant|revoke)\b`)

// sqlBlocksOf returns the GRANT lines of every ```sql block of doc that
// grant to mtix_writer, in order, with the connect grant left out (the
// skill's role-creation block holds it; the security model holds none). The
// test fails on any other GRANT or REVOKE line in a block, except the
// skill's role-creation grants (schema CREATE and CONNECT).
func sqlBlocksOf(t *testing.T, doc string) []string {
	t.Helper()
	var out []string
	for _, block := range regexp.MustCompile("(?s)```sql\n(.*?)```").FindAllStringSubmatch(doc, -1) {
		for _, line := range strings.Split(block[1], "\n") {
			if !grantLike.MatchString(line) {
				continue
			}
			switch {
			case strings.HasPrefix(line, "GRANT ") && strings.Contains(line, " TO mtix_writer;") &&
				!strings.HasPrefix(line, "GRANT CONNECT"):
				require.NotNilf(t, writerGrantStatement.FindStringSubmatch(line),
					"unrecognised grant to mtix_writer: %q", line)
				out = append(out, line)
			case strings.HasPrefix(line, "GRANT CONNECT ON DATABASE mtix_hub TO mtix_writer;"),
				strings.HasPrefix(line, "GRANT USAGE, CREATE ON SCHEMA public TO mtix_owner;"),
				strings.HasPrefix(line, "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"):
				// the skill's role-creation statements
			default:
				require.Failf(t, "unexpected grant or revoke in a documented sql block", "%q", line)
			}
		}
	}
	return out
}

// TestLeastPrivilegeGrantDocs_SecurityModelMatchesSkillAndProvenSet: the
// least-privilege list and SQL of docs/SECURITY-MODEL.md name the grant set
// that expectedSyncGrants holds, the set TestLeastPrivilegeWriterRole_
// PushPullClone proves on a live hub, and carry the sync skill's GRANT
// statements verbatim, so the two documents cannot drift (MTIX-95.8.4).
// It needs no database.
func TestLeastPrivilegeGrantDocs_SecurityModelMatchesSkillAndProvenSet(t *testing.T) {
	security := readRepoFile(t, securityModelPath)
	skill := readRepoFile(t, syncSkillPath)

	require.ElementsMatch(t, expectedSyncGrants(), documentedGrantsIn(t, security),
		"the security model's list names exactly the proven set")
	require.ElementsMatch(t, expectedSyncGrants(), skillWriterGrants(t, security),
		"the security model's SQL grants exactly the proven set")
	require.Equal(t, sqlBlocksOf(t, skill), sqlBlocksOf(t, security),
		"the security model carries the skill's GRANT statements verbatim")
}
