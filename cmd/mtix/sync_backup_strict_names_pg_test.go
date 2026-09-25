// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// PG-gated tests of mtix sync backup with the real pg_dump against a hub
// that lacks a table under its name: the backup fails, leaves no file and
// says how to fix the hub (MTIX-95.7.4).

// backupTablesRE matches the table list of the backup's success message.
var backupTablesRE = regexp.MustCompile(`\(tables: ([^)]*)\)`)

// reportedBackupTables returns the sorted tables the backup's success
// message lists.
func reportedBackupTables(t *testing.T, stdout string) []string {
	t.Helper()
	m := backupTablesRE.FindStringSubmatch(stdout)
	require.NotNil(t, m, "the success message lists the tables: %s", stdout)
	listed := strings.Split(m[1], ", ")
	sort.Strings(listed)
	return listed
}

// TestBackup_HubLacksATable_FailsWithInitHintAndNoFile: each hub table in
// turn is made absent under its name, and a hub from before a migration
// lacks node_renumber_remaps. Each time the backup fails, pg_dump names
// the table, the error says to run mtix sync init, and no file is left.
// After mtix sync init the backup succeeds, and its message lists exactly
// the tables its dump creates, which are the hub tables (MTIX-95.7.4).
func TestBackup_HubLacksATable_FailsWithInitHintAndNoFile(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	dsn := f.dbURL.String()
	requirePgDumpForServer(t, dsn)
	t.Setenv(transport.EnvDSN, dsn)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts), stderr.String())
	tables, err := migrations.Tables()
	require.NoError(t, err)
	const initHint = "run mtix sync init, with the DSN naming the table owner, then back up again"

	requireFailsFor := func(table string) {
		t.Helper()
		out := filepath.Join(t.TempDir(), "hub.sql")
		stdoutText, errText, err := runBackupCLI(t, dsn, out, "--insecure-tls")
		require.Error(t, err, "a hub without %s fails the backup", table)
		require.NotContains(t, stdoutText, "backup written", table)
		require.Contains(t, errText, `no matching tables were found for pattern "`+table+`"`, "pg_dump names %s", table)
		require.Contains(t, err.Error(), initHint, table)
		require.NoFileExists(t, out, "the partial dump of a hub without %s is removed", table)
	}
	for _, table := range tables {
		f.ddl("ALTER TABLE public.%I RENAME TO %I", table, table+"_elsewhere")
		requireFailsFor(table)
		f.ddl("ALTER TABLE public.%I RENAME TO %I", table+"_elsewhere", table)
	}

	f.exec(`DROP TABLE public.node_renumber_remaps`)
	requireFailsFor("node_renumber_remaps")

	stdout.Reset()
	stderr.Reset()
	require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts), stderr.String())
	out := filepath.Join(t.TempDir(), "hub.sql")
	stdoutText, errText, err := runBackupCLI(t, dsn, out, "--insecure-tls")
	require.NoError(t, err, errText)
	body, err := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	require.Equal(t, dumpedTables(string(body)), reportedBackupTables(t, stdoutText),
		"the message lists exactly the tables the dump creates")
	require.Equal(t, tables, dumpedTables(string(body)), "the dump creates every hub table")
}

// TestBackup_HubSchemaOnlyInDSN_AdviceNamesTheDSNRole: the hub's tables
// are in schema hub_data, owned by another role, and only the DSN's
// options put hub_data on the search_path. pg_dump, which receives no
// options, does not see them: the backup fails with the search_path step
// for the role the DSN names, quoted, and leaves no file. Setting the
// owner's search_path changes nothing; setting the DSN role's makes the
// backup dump every hub table in hub_data. The settings are made per
// database (ALTER ROLE ... IN DATABASE), the same role default pg_dump
// reads at login, so the server's other databases are not affected
// (MTIX-95.7.4).
func TestBackup_HubSchemaOnlyInDSN_AdviceNamesTheDSNRole(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	requirePgDumpForServer(t, f.dbURL.String())
	owner := f.ownerRole()
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", "hub_data", owner)
	u := f.dbURL
	q := u.Query()
	q.Set("options", "-c role="+owner+" -c search_path=hub_data")
	u.RawQuery = q.Encode()
	dsn := u.String()
	t.Setenv(transport.EnvDSN, dsn)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts), stderr.String())
	tables, err := migrations.Tables()
	require.NoError(t, err)
	require.Equal(t, tables, f.strings(`SELECT c.relname::text FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'hub_data' AND c.relkind = 'r' ORDER BY 1`), "the hub lives in hub_data")

	backup := func() (string, string, error) {
		out := filepath.Join(t.TempDir(), "hub.sql")
		_, errText, err := runBackupCLI(t, dsn, out, "--insecure-tls")
		if err != nil {
			require.NoFileExists(t, out, "a failed backup leaves no file")
		}
		return out, errText, err
	}
	_, _, err = backup()
	require.Error(t, err, "pg_dump does not see hub_data")
	require.Contains(t, err.Error(), `ALTER ROLE "`+f.superuser+`" SET search_path = <schema>, public`,
		"the step names the role the DSN names, which pg_dump connects as")
	require.NotContains(t, err.Error(), owner, "not the table owner")

	f.ddl("ALTER ROLE %I IN DATABASE %I SET search_path = hub_data, public", owner, f.dbName)
	_, _, err = backup()
	require.Error(t, err, "the owner's search_path does not reach pg_dump")

	f.ddl("ALTER ROLE %I IN DATABASE %I SET search_path = hub_data, public", f.superuser, f.dbName)
	out, errText, err := backup()
	require.NoError(t, err, errText)
	body, err := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	require.Equal(t, tables, dumpedTables(string(body)), "the dump creates every hub table")
	require.Regexp(t, `(?m)^CREATE TABLE hub_data\.sync_events \(`, string(body), "from hub_data")
}
