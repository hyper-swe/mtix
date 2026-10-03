// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// syncSkillPath is the sync skill template, whose list and SQL block state
// the writer role's grants (MTIX-95.8.1).
const syncSkillPath = "internal/docs/templates/skills/sync.md.tmpl"

// writerGrantStatement is the form of every GRANT the skill's SQL gives the
// writer role: the privilege, the object kind, the names, and for a grant a
// command scopes, the trailing comment that says which command.
var writerGrantStatement = regexp.MustCompile("^GRANT (SELECT|INSERT|UPDATE|USAGE|EXECUTE) ON (SCHEMA|TABLE|SEQUENCE|FUNCTION) " +
	"([a-z_]+(?:, [a-z_]+)*) TO mtix_writer;(?: -- only for a role that runs `([^`]+)`(" +
	regexp.QuoteMeta(unindexedHub) + ")?)?$")

// skillWriterGrants parses the GRANT statements for mtix_writer in the
// skill's SQL blocks into the documented-grant form. Any GRANT of those
// privileges to mtix_writer that does not match the form fails the test, so
// the SQL names nothing the test does not check (MTIX-95.8.1).
func skillWriterGrants(t *testing.T, skill string) []documentedGrant {
	t.Helper()
	var out []documentedGrant
	for _, block := range regexp.MustCompile("(?s)```sql\n(.*?)```").FindAllStringSubmatch(skill, -1) {
		for _, line := range strings.Split(block[1], "\n") {
			if !strings.HasPrefix(line, "GRANT ") || !strings.Contains(line, " TO mtix_writer;") ||
				strings.HasPrefix(line, "GRANT CONNECT") {
				continue
			}
			m := writerGrantStatement.FindStringSubmatch(line)
			require.NotNilf(t, m, "unrecognised GRANT in the skill's SQL: %q", line)
			scope := m[4] + m[5]
			if m[2] == "SCHEMA" {
				out = append(out, documentedGrant{m[1], "SCHEMA", "", scope})
				continue
			}
			for _, name := range strings.Split(m[3], ", ") {
				out = append(out, documentedGrant{m[1], m[2], name, scope})
			}
		}
	}
	require.NotEmpty(t, out, "the skill's SQL grants the writer role")
	return out
}

// TestLeastPrivilegeWriterRole_PushPullClone: the sync skill's list and its
// SQL name exactly the expected set for the writer role. On a hub in a
// schema PUBLIC cannot use, a login role holding the SQL's grants (no
// scoped extra) pushes the local events through `mtix sync push`, pulls
// them back into an emptied store with `mtix sync pull`, and rebuilds the
// store with `mtix sync clone`; the same role is refused every DDL
// statement and every change to the hub's event log (MTIX-95.8.1).
func TestLeastPrivilegeWriterRole_PushPullClone(t *testing.T) {
	skill := readRepoFile(t, syncSkillPath)
	fromList := documentedGrantsIn(t, skill)
	fromSQL := skillWriterGrants(t, skill)
	require.ElementsMatch(t, expectedSyncGrants(), fromList, "the skill's list names exactly the set")
	require.ElementsMatch(t, fromList, fromSQL, "the skill's SQL grants what its list names")

	initTestApp(t)
	h := newLeastPrivilegeHub(t, fromSQL)
	t.Setenv(transport.EnvDSN, h.syncDSN)
	opts := transport.Options{InsecureTLS: true}
	var stdout, stderr bytes.Buffer
	run := func(what string, fn func() error) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		require.NoErrorf(t, fn(), "%s as the writer role: %s", what, stderr.String())
	}

	seedLocal(t, "alpha", "beta")
	run("push", func() error { return runSyncPush(h.ctx, &stdout, &stderr, nil, opts, false) })
	require.Equal(t, []string{"2"}, h.f.strings(`SELECT count(*)::text FROM hub_data.sync_events`), "the hub holds both creates")

	resetLocalForFreshPull(t)
	run("pull", func() error { return runSyncPull(h.ctx, &stdout, &stderr, nil, opts, 1000) })
	requireLiveTitles(t, "alpha", "beta")

	resetLocalForFreshPull(t)
	run("clone", func() error { return runSyncClone(h.ctx, &stdout, &stderr, nil, opts, false, 1000) })
	requireLiveTitles(t, "alpha", "beta")

	requireWriterRefusedDDL(t, h)
}

// requireLiveTitles fails unless the local store's live nodes carry exactly
// the titles.
func requireLiveTitles(t *testing.T, want ...string) {
	t.Helper()
	got, err := queryLiveNodeTitles(context.Background())
	require.NoError(t, err)
	var titles []string
	for _, title := range got {
		titles = append(titles, title)
	}
	require.ElementsMatch(t, want, titles)
}

// requireWriterRefusedDDL fails unless PostgreSQL refuses the writer role
// each statement: creating objects in the hub's schema, altering, dropping
// or truncating a sync table, and deleting from the event log.
func requireWriterRefusedDDL(t *testing.T, h *leastPrivilegeHub) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE hub_data.stray (id int)`,
		`CREATE FUNCTION hub_data.stray() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`ALTER TABLE hub_data.sync_events ADD COLUMN stray int`,
		`DROP TABLE hub_data.sync_events`,
		`TRUNCATE hub_data.sync_events`,
		`DELETE FROM hub_data.sync_events`,
	} {
		_, err := h.syncPool.Inner().Exec(h.ctx, stmt)
		require.Errorf(t, err, "the writer role is refused: %s", stmt)
		require.Regexpf(t, "permission denied|must be owner", err.Error(), "%s", stmt)
	}
}
