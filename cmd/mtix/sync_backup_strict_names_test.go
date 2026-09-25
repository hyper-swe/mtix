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
)

// These tests run mtix sync backup against the stand-in pg_dump of
// sync_backup_posture_test.go: every hub table name must match a table,
// and a backup that pg_dump stops because one does not says how to fix
// the hub (MTIX-95.7.4).

// backupDSNFor returns a stand-in hub DSN whose role is user.
func backupDSNFor(user string) string {
	return "postgres://" + url.UserPassword(user, backupTestPassword).String() + "@db.example.invalid:5432/hub"
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
// the DSN, set the search_path of the DSN's role (quoted, so it runs as
// printed). Any other pg_dump failure carries neither step (MTIX-95.7.4).
func TestBackup_PgDumpFails_HintOnlyWhenATableIsNotFound(t *testing.T) {
	const initHint = "run mtix sync init, with the DSN naming the table owner, then back up again"
	tests := []struct {
		name     string
		user     string
		pgStderr string
		wantRole string // "" when no hint is expected
	}{
		{"one table name matches no table", "backup_user",
			`pg_dump: error: no matching tables were found for pattern "node_renumber_remaps"`, `"backup_user"`},
		{"no table name matches a table", "backup_user",
			`pg_dump: error: no matching tables were found`, `"backup_user"`},
		{"a role name that needs quoting", `Ops"Backup`,
			`pg_dump: error: no matching tables were found for pattern "audit_log"`, `"Ops""Backup"`},
		{"a connection failure", "backup_user",
			`pg_dump: error: connection to server at "db.example.invalid" failed: FATAL:  password authentication failed`, ""},
		{"a failure with no message", "backup_user", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			fake := installFakePgDump(t)
			t.Setenv("MTIX_TEST_PGDUMP_FAIL", "1")
			t.Setenv("MTIX_TEST_PGDUMP_STDERR", tt.pgStderr)
			out := filepath.Join(t.TempDir(), "hub.sql")

			_, stderr, err := runBackupCLI(t, backupDSNFor(tt.user), out)
			require.Error(t, err)
			require.True(t, fake.ran())
			require.NoFileExists(t, out, "a failed backup leaves no file")
			require.Contains(t, err.Error(), "pg_dump failed")
			require.Contains(t, stderr, tt.pgStderr, "pg_dump's own message is shown")
			msg := err.Error()
			if tt.wantRole == "" {
				require.NotContains(t, msg, "mtix sync init", "only a table that is not found points at init")
				require.NotContains(t, msg, "ALTER ROLE", "only a table that is not found points at the search_path")
				return
			}
			require.Contains(t, msg, "a hub table was not found")
			require.Contains(t, msg, initHint)
			require.Contains(t, msg, "ALTER ROLE "+tt.wantRole+" SET search_path = <schema>, public")
			require.NotContains(t, msg, "<owner>")
		})
	}
}

// TestWithSearchPathAdvice_Role_QuotedOrPlaceholder: the search_path step
// is added only to a backup that failed because a hub table was not found;
// it quotes the role as an identifier, and names a placeholder when the
// role is not known (MTIX-95.7.4).
func TestWithSearchPathAdvice_Role_QuotedOrPlaceholder(t *testing.T) {
	notFound := fmt.Errorf("mtix sync backup: pg_dump failed: %w", errHubTableNotFound)
	other := errors.New("mtix sync backup: pg_dump failed: exit status 1")
	tests := []struct {
		name string
		err  error
		role string
		want string // "" when err is returned unchanged
	}{
		{"a plain role", notFound, "alice", `ALTER ROLE "alice" SET search_path = <schema>, public`},
		{"a role that needs quoting", notFound, `Ops "Backup"`, `ALTER ROLE "Ops ""Backup""" SET search_path = <schema>, public`},
		{"no role known", notFound, "", `ALTER ROLE <the DSN's role> SET search_path = <schema>, public`},
		{"another failure", other, "alice", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withSearchPathAdvice(tt.err, tt.role)
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

// TestSyncBackupCmd_Help_SearchPathAdviceNamesTheDSNRole: the backup's
// help names the DSN's role, whose default search_path pg_dump uses, in
// its search_path step, not the table owner, and says that a hub lacking
// a table fails the backup (MTIX-95.7.4).
func TestSyncBackupCmd_Help_SearchPathAdviceNamesTheDSNRole(t *testing.T) {
	long := strings.Join(strings.Fields(newSyncBackupCmd().Long), " ")
	require.Contains(t, long, "ALTER ROLE <the DSN's role> SET search_path = <schema>, public")
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
