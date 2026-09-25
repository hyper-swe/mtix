// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// These tests run mtix sync backup against the stand-in pg_dump of
// sync_backup_posture_test.go: every hub table name must match a table,
// and a backup that pg_dump stops because one does not says how to fix
// the hub (MTIX-95.7.4).

// backupDSNFor returns a stand-in hub DSN whose role is user, for the
// database hub.
func backupDSNFor(user string) string {
	return backupDSNForDB(user, "hub")
}

// backupDSNForDB returns a stand-in hub DSN whose role is user, for the
// database db.
func backupDSNForDB(user, db string) string {
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, backupTestPassword),
		Host: "db.example.invalid:5432", Path: "/" + db}
	return u.String()
}

// TestBackup_TranslatedLocale_PgDumpMessagesInEnglish: pg_dump runs with
// LC_MESSAGES=C and without LC_ALL and LANGUAGE, whatever the caller's
// locale, so its messages, which the backup reads to give its hint, are
// the untranslated ones (MTIX-95.7.4).
func TestBackup_TranslatedLocale_PgDumpMessagesInEnglish(t *testing.T) {
	tests := []struct {
		name   string
		locale map[string]string
	}{
		{"a translated locale", map[string]string{"LANG": "de_DE.UTF-8", "LC_ALL": "de_DE.UTF-8",
			"LC_MESSAGES": "de_DE.UTF-8", "LANGUAGE": "de"}},
		{"no locale set", map[string]string{"LANG": "", "LC_ALL": "", "LC_MESSAGES": "", "LANGUAGE": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			fake := installFakePgDump(t)
			for k, v := range tt.locale {
				t.Setenv(k, v)
			}
			out := filepath.Join(t.TempDir(), "hub.sql")
			_, stderr, err := runBackupCLI(t, backupDSNFor("backup_user"), out)
			require.NoError(t, err, stderr)
			env := fake.env(t)
			require.Equal(t, "C", env["LC_MESSAGES"], "pg_dump's messages are untranslated")
			require.NotContains(t, env, "LC_ALL", "LC_ALL would override LC_MESSAGES")
			require.NotContains(t, env, "LANGUAGE", "LANGUAGE would choose a translation")
		})
	}
}

// TestBackup_PgDumpArgs_EveryTableNameMustMatch: pg_dump runs with
// --strict-names, so a --table name that matches no table stops the dump
// instead of being skipped (MTIX-95.7.4).
func TestBackup_PgDumpArgs_EveryTableNameMustMatch(t *testing.T) {
	initTestApp(t)
	fake := installFakePgDump(t)
	out := filepath.Join(t.TempDir(), "hub.sql")
	_, stderr, err := runBackupCLI(t, backupDSNFor("backup_user"), out)
	require.NoError(t, err, stderr)
	require.Contains(t, fake.argv(t), "--strict-names")
}

// TestBackup_PgDumpFails_HintOnlyWhenATableIsNotFound: when pg_dump
// reports a hub table name that matches no table, the failed backup
// leaves no file and its error says how to fix the hub: run mtix sync
// init as the table owner, or, for a hub whose schema is named only in
// the DSN, set the search_path of the DSN's role in the DSN's database,
// which takes precedence over the role-wide setting it also names (both
// names quoted, so the statements run as printed), or, for a DSN role
// without USAGE on the hub's schema, grant it USAGE on the schema and
// SELECT on each sync table and sync-table sequence by name. Any other
// pg_dump failure carries none of these steps (MTIX-95.7.4, MTIX-95.1.4).
func TestBackup_PgDumpFails_HintOnlyWhenATableIsNotFound(t *testing.T) {
	const initHint = "run mtix sync init, with the DSN naming the table owner, then back up again"
	tests := []struct {
		name     string
		user, db string
		pgStderr string
		wantRole string // "" when no hint is expected
		wantDB   string
	}{
		{"one table name matches no table", "backup_user", "hub",
			`pg_dump: error: no matching tables were found for pattern "node_renumber_remaps"`, `"backup_user"`, `"hub"`},
		{"no table name matches a table", "backup_user", "hub",
			`pg_dump: error: no matching tables were found`, `"backup_user"`, `"hub"`},
		{"names that need quoting", `Ops"Backup`, `Hub "Data"`,
			`pg_dump: error: no matching tables were found for pattern "audit_log"`, `"Ops""Backup"`, `"Hub ""Data"""`},
		{"a connection failure", "backup_user", "hub",
			`pg_dump: error: connection to server at "db.example.invalid" failed: FATAL:  password authentication failed`, "", ""},
		{"a failure with no message", "backup_user", "hub", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			fake := installFakePgDump(t)
			t.Setenv("MTIX_TEST_PGDUMP_FAIL", "1")
			t.Setenv("MTIX_TEST_PGDUMP_STDERR", tt.pgStderr)
			out := filepath.Join(t.TempDir(), "hub.sql")

			_, stderr, err := runBackupCLI(t, backupDSNForDB(tt.user, tt.db), out)
			require.Error(t, err)
			require.True(t, fake.ran())
			require.NoFileExists(t, out, "a failed backup leaves no file")
			require.Contains(t, err.Error(), "pg_dump failed")
			require.Contains(t, stderr, tt.pgStderr, "pg_dump's own message is shown")
			msg := err.Error()
			if tt.wantRole == "" {
				require.NotContains(t, msg, "mtix sync init", "only a table that is not found points at init")
				require.NotContains(t, msg, "ALTER ROLE", "only a table that is not found points at the search_path")
				require.NotContains(t, msg, "GRANT", "only a table that is not found points at schema access")
				return
			}
			require.Contains(t, msg, "a hub table was not found")
			require.Contains(t, msg, initHint)
			require.Contains(t, msg, "ALTER ROLE "+tt.wantRole+" IN DATABASE "+tt.wantDB+
				" SET search_path = <schema>, public (this database only; it takes precedence over")
			require.Contains(t, msg, "ALTER ROLE "+tt.wantRole+" SET search_path = <schema>, public")
			require.NotContains(t, msg, "<owner>")
			tables, err := migrations.Tables()
			require.NoError(t, err)
			require.Contains(t, msg, "if the DSN's role lacks USAGE on the hub's schema, which leaves the schema off its "+
				"search_path, the table owner runs GRANT USAGE ON SCHEMA <schema> TO "+tt.wantRole+"; "+
				"GRANT SELECT ON TABLE <schema>."+strings.Join(tables, ", <schema>.")+" TO "+tt.wantRole+"; "+
				"GRANT SELECT ON SEQUENCE <schema>.audit_log_audit_id_seq, <schema>.sync_conflicts_conflict_id_seq, "+
				"<schema>.sync_node_collisions_collision_id_seq TO "+tt.wantRole+"; then back up again")
			require.NotContains(t, msg, "ALL TABLES", "the grant names each table")
		})
	}
}

// TestWithSearchPathAdvice_Role_QuotedOrPlaceholder: the search_path step
// is added only to a backup that failed because a hub table was not found;
// it quotes the role and the database as identifiers, and names a
// placeholder for either only when it is not known (MTIX-95.7.4).
func TestWithSearchPathAdvice_Role_QuotedOrPlaceholder(t *testing.T) {
	notFound := fmt.Errorf("mtix sync backup: pg_dump failed: %w", errHubTableNotFound)
	other := errors.New("mtix sync backup: pg_dump failed: exit status 1")
	tests := []struct {
		name     string
		err      error
		role, db string
		want     string // "" when err is returned unchanged
	}{
		{"plain names", notFound, "alice", "hub",
			`ALTER ROLE "alice" IN DATABASE "hub" SET search_path = <schema>, public (this database only; ` +
				`it takes precedence over a role-wide ALTER ROLE "alice" SET search_path = <schema>, public)`},
		{"names that need quoting", notFound, `Ops "Backup"`, `Hub Data`,
			`ALTER ROLE "Ops ""Backup""" IN DATABASE "Hub Data" SET search_path = <schema>, public (this database only; ` +
				`it takes precedence over a role-wide ALTER ROLE "Ops ""Backup""" SET search_path = <schema>, public)`},
		{"no role known", notFound, "", "hub",
			`ALTER ROLE <the DSN's role> IN DATABASE "hub" SET search_path = <schema>, public`},
		{"no database known", notFound, "alice", "",
			`ALTER ROLE "alice" IN DATABASE <the DSN's database> SET search_path = <schema>, public`},
		{"another failure", other, "alice", "hub", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withSearchPathAdvice(tt.err, tt.role, tt.db)
			if tt.want == "" {
				require.Same(t, tt.err, got, "any other error is returned unchanged")
				return
			}
			require.ErrorIs(t, got, errHubTableNotFound)
			require.True(t, strings.HasPrefix(got.Error(), tt.err.Error()+"; "), got.Error())
			require.Contains(t, got.Error(), tt.want)
		})
	}
}

// TestWithSchemaUsageAdvice_Grants_ByNameOrUnchanged: the schema-usage
// step is added only to a backup that failed because a hub table was not
// found; it grants USAGE on the schema and SELECT on each named table and
// sequence, leaves out the sequence grant when there is no sequence, and
// names a placeholder only when the role is not known (MTIX-95.1.4).
func TestWithSchemaUsageAdvice_Grants_ByNameOrUnchanged(t *testing.T) {
	notFound := fmt.Errorf("mtix sync backup: pg_dump failed: %w", errHubTableNotFound)
	other := errors.New("mtix sync backup: pg_dump failed: exit status 1")
	tests := []struct {
		name              string
		err               error
		role              string
		tables, sequences []string
		want              string // "" when err is returned unchanged
		absent            string
	}{
		{"tables and sequences", notFound, "reader", []string{"a", "b"}, []string{"a_id_seq"},
			`the table owner runs GRANT USAGE ON SCHEMA <schema> TO "reader"; GRANT SELECT ON TABLE <schema>.a, ` +
				`<schema>.b TO "reader"; GRANT SELECT ON SEQUENCE <schema>.a_id_seq TO "reader"; then back up again`, ""},
		{"no sequence", notFound, "reader", []string{"a"}, nil,
			`GRANT SELECT ON TABLE <schema>.a TO "reader"; then back up again`, "SEQUENCE"},
		{"no role known", notFound, "", []string{"a"}, nil,
			`GRANT USAGE ON SCHEMA <schema> TO <the DSN's role>; GRANT SELECT ON TABLE <schema>.a TO <the DSN's role>;`, ""},
		{"another failure", other, "reader", []string{"a"}, nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withSchemaUsageAdvice(tt.err, tt.role, tt.tables, tt.sequences)
			if tt.want == "" {
				require.Same(t, tt.err, got, "any other error is returned unchanged")
				return
			}
			require.ErrorIs(t, got, errHubTableNotFound)
			require.True(t, strings.HasPrefix(got.Error(), tt.err.Error()+"; "), got.Error())
			require.Contains(t, got.Error(), tt.want)
			if tt.absent != "" {
				require.NotContains(t, got.Error(), tt.absent)
			}
		})
	}
}

// TestSyncBackupCmd_Help_SearchPathAdviceNamesTheDSNRole: the backup's
// help names the DSN's role, whose default search_path pg_dump uses, in
// its search_path step, not the table owner, and says that a hub lacking
// a table fails the backup (MTIX-95.7.4).
func TestSyncBackupCmd_Help_SearchPathAdviceNamesTheDSNRole(t *testing.T) {
	long := strings.Join(strings.Fields(newSyncBackupCmd().Long), " ")
	require.Contains(t, long, "ALTER ROLE <the DSN's role> IN DATABASE <the DSN's database> SET search_path = <schema>, public")
	require.Contains(t, long, "If the role the DSN names lacks USAGE on the hub's schema, pg_dump does not see the "+
		"tables either: the failed backup prints the GRANT statements, naming each sync table and sequence, that the "+
		"table owner runs.")
	require.Contains(t, long, "takes precedence over a role-wide ALTER ROLE <the DSN's role> SET search_path = <schema>, public")
	require.NotContains(t, long, "ALTER ROLE <owner>")
	require.Contains(t, long, "a hub that lacks one")
	require.Contains(t, long, "fails the backup with a hint to run mtix sync init")
}

// TestSyncBackupCmd_CLIReference_MatchesHelp: docs/CLI_REFERENCE.md
// carries the backup command's help exactly as the command prints it
// (MTIX-95.7.4).
func TestSyncBackupCmd_CLIReference_MatchesHelp(t *testing.T) {
	cmd := newSyncBackupCmd()
	ref, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	section := "## backup\n\n**Usage:** `backup`\n\n" + cmd.Short + "\n\n" + cmd.Long + "\n"
	require.Contains(t, string(ref), section, "docs/CLI_REFERENCE.md matches mtix sync backup --help")
}
