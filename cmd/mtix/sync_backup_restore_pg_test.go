// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// PG-gated tests of mtix sync backup against a real hub with the real
// pg_dump, and of the restore runbook: restore the dump into an empty
// database, run mtix sync init as the table owner, and mtix sync doctor
// reports every mtix function and trigger present and enabled
// (MTIX-95.7, F-39).

// hubPool opens a plain pool on the test hub for setup and assertions.
func hubPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// requirePsql returns psql's path, or skips the test with the reason
// recorded when psql is not on PATH.
func requirePsql(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql not on PATH: the restore runbook test needs pg_dump and psql; install the PostgreSQL client tools to run it")
	}
	return path
}

// createTableRE matches each CREATE TABLE statement of a plain pg_dump.
var createTableRE = regexp.MustCompile(`(?m)^CREATE TABLE (?:[a-z_][a-z0-9_]*\.)?([a-z_][a-z0-9_]*) \(`)

// dumpedTables returns the sorted names of the tables a plain dump
// creates.
func dumpedTables(dump string) []string {
	var out []string
	for _, m := range createTableRE.FindAllStringSubmatch(dump, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// TestBackup_TableSetEqualsMigrationTables: the dump of a migrated hub
// creates exactly the tables the hub migrations create, restore-epoch
// state included (MTIX-95.7, D18).
func TestBackup_TableSetEqualsMigrationTables(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	requirePgDumpForServer(t, dsn)

	out := filepath.Join(t.TempDir(), "hub.sql")
	stdout, stderr, err := runBackupCLI(t, dsn, out, "--insecure-tls")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "backup written to")

	body, err := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	want, err := migrations.Tables()
	require.NoError(t, err)
	require.Equal(t, want, dumpedTables(string(body)), "the dump holds every hub table and nothing else")
	require.Regexp(t, `(?m)^COPY (?:[a-z_]+\.)?sync_hub_state `, string(body), "the restore epoch is in the dump")
}

// withoutSSLMode returns dsn with its sslmode setting removed.
func withoutSSLMode(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Del("sslmode")
	u.RawQuery = q.Encode()
	return u.String()
}

// TestBackup_DSNWithoutSSLMode_NeedsTLSFromServer: a DSN that names no
// sslmode connects pg_dump with verify-full, exactly as sync connects,
// so a hub that offers no TLS is refused; with sslmode=disable and
// --insecure-tls the same loopback hub backs up (FR-18.15, MTIX-95.7).
func TestBackup_DSNWithoutSSLMode_NeedsTLSFromServer(t *testing.T) {
	dsn := requireCmdPG(t)
	pool := openCmdHub(t)
	initTestApp(t)
	requirePgDumpForServer(t, dsn)
	var ssl string
	require.NoError(t, pool.Inner().QueryRow(context.Background(), `SHOW ssl`).Scan(&ssl))
	if ssl == "on" {
		t.Skip("the test hub offers TLS; this case needs a hub without TLS")
	}

	out := filepath.Join(t.TempDir(), "hub.sql")
	_, stderr, err := runBackupCLI(t, withoutSSLMode(t, dsn), out)
	require.Error(t, err, "verify-full cannot connect to a hub without TLS")
	require.Contains(t, stderr, "SSL", "pg_dump reports that TLS was required")
	require.NoFileExists(t, out, "a failed backup leaves no file")

	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("sslmode", "disable")
	u.RawQuery = q.Encode()
	out = filepath.Join(t.TempDir(), "hub.sql")
	_, stderr, err = runBackupCLI(t, u.String(), out, "--insecure-tls")
	require.NoError(t, err, stderr)
	require.FileExists(t, out)
}

// psqlEnv returns the PG* variables that connect psql to the test hub.
func psqlEnv(t *testing.T, dsn string) []string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	pw, _ := u.User.Password()
	env := append(os.Environ(),
		"PGHOST="+u.Hostname(), "PGPORT="+u.Port(), "PGUSER="+u.User.Username(),
		"PGPASSWORD="+pw, "PGDATABASE="+strings.TrimPrefix(u.Path, "/"))
	for k, envName := range map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT"} {
		if v := u.Query().Get(k); v != "" {
			env = append(env, envName+"="+v)
		}
	}
	return env
}

// restoreWithPsql replays the dump into the hub with psql, the runbook's
// first step, and returns psql's output.
func restoreWithPsql(t *testing.T, psql, dsn, dump string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, psql, "-X", "-q", "-f", dump) //nolint:gosec // psql from LookPath, dump from t.TempDir()
	cmd.Env = psqlEnv(t, dsn)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// dropMtixFunctions drops every function the migrations create (and so
// every trigger that uses one), leaving no mtix object behind once
// freshCmdHub has dropped the tables.
func dropMtixFunctions(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	functions, err := migrations.FunctionSignatures()
	require.NoError(t, err)
	for _, fn := range functions {
		var stmt string
		// Quote the function name server-side (directive SQL Rule 1a);
		// the argument types are the migrations' own constant text.
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT format('DROP FUNCTION IF EXISTS %I(%s) CASCADE', $1::text, $2::text)`, fn.Name, fn.Args).Scan(&stmt))
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err)
	}
}

// hubObjectCounts returns how many of the migrations' tables and
// functions exist on the hub.
func hubObjectCounts(t *testing.T, pool *pgxpool.Pool) (tables, functions int) {
	t.Helper()
	ctx := context.Background()
	tableNames, err := migrations.Tables()
	require.NoError(t, err)
	signatures, err := migrations.FunctionSignatures()
	require.NoError(t, err)
	functionSigs := make([]string, 0, len(signatures))
	for _, fn := range signatures {
		functionSigs = append(functionSigs, fn.Signature())
	}
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM unnest($1::text[]) AS t(name) WHERE pg_catalog.to_regclass(t.name) IS NOT NULL),
		       (SELECT count(*) FROM unnest($2::text[]) AS f(sig) WHERE pg_catalog.to_regprocedure(f.sig) IS NOT NULL)`,
		tableNames, functionSigs).Scan(&tables, &functions))
	return tables, functions
}

// hubRowCounts returns the row count of every hub table.
func hubRowCounts(t *testing.T, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	tables, err := migrations.Tables()
	require.NoError(t, err)
	out := map[string]int64{}
	for _, tbl := range tables {
		var stmt string
		// Quote the table name server-side (directive SQL Rule 1a).
		require.NoError(t, pool.QueryRow(ctx, `SELECT format('SELECT count(*) FROM %I', $1::text)`, tbl).Scan(&stmt))
		var n int64
		require.NoError(t, pool.QueryRow(ctx, stmt).Scan(&n))
		out[tbl] = n
	}
	return out
}

// requireEveryTriggerEnabled asserts that every trigger the migrations
// define exists and fires in normal sessions (tgenabled 'O').
func requireEveryTriggerEnabled(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	triggers, err := migrations.Triggers()
	require.NoError(t, err)
	for _, tr := range triggers {
		var enabled string
		require.NoErrorf(t, pool.QueryRow(context.Background(), `
			SELECT t.tgenabled::text FROM pg_catalog.pg_trigger t
			WHERE t.tgrelid = pg_catalog.to_regclass($1) AND t.tgname = $2 AND NOT t.tgisinternal`,
			tr.Table, tr.Name).Scan(&enabled), "trigger %s on %s", tr.Name, tr.Table)
		require.Equalf(t, "O", enabled, "trigger %s on %s", tr.Name, tr.Table)
	}
}

// TestRestoreRunbook_EmptyDB_DoctorGreen follows the documented restore
// runbook: back up a populated hub, empty the database, restore the dump
// with psql, run mtix sync init as the table owner, then mtix sync
// doctor. Before init the doctor names the missing functions and triggers
// and the fix; after it every mtix function and trigger is present and
// enabled, the doctor exits 0, and the data and restore epoch are those of
// the backup (MTIX-95.7, F-39).
func TestRestoreRunbook_EmptyDB_DoctorGreen(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	requirePgDumpForServer(t, dsn)
	psql := requirePsql(t)
	ctx := context.Background()
	pool := hubPool(t, dsn)

	seedLocal(t, "restore-a", "restore-b")
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())
	pushLocal(t, dsn)
	_, err := pool.Exec(ctx, `UPDATE sync_hub_state SET restore_epoch = 7`)
	require.NoError(t, err)
	before := hubRowCounts(t, pool)
	require.Positive(t, before["sync_events"])
	require.Positive(t, before["sync_projects"], "init registered the project")

	dump := filepath.Join(t.TempDir(), "hub.sql")
	_, errText, err := runBackupCLI(t, dsn, dump, "--insecure-tls")
	require.NoError(t, err, errText)

	freshCmdHub(t, dsn)
	dropMtixFunctions(t, pool)
	tables, functions := hubObjectCounts(t, pool)
	require.Zero(t, tables, "the database holds no mtix table before the restore")
	require.Zero(t, functions, "the database holds no mtix function before the restore")

	psqlOut := restoreWithPsql(t, psql, dsn, dump)
	t.Logf("psql restore output:\n%s", psqlOut)

	report, err := runDoctorReport(t)
	require.NoError(t, err, "missing triggers are a WARN by default")
	pass, warn, detail, fix := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass)
	require.True(t, warn, "after the restore alone the triggers are missing: %s", detail)
	require.Contains(t, detail, "audit_log_immutable")
	require.Contains(t, fix, "mtix sync init")

	stdout.Reset()
	stderr.Reset()
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())

	report, err = runDoctorReport(t)
	require.NoError(t, err, "doctor is green after the runbook")
	require.True(t, report.Pass)
	pass, warn, detail, fix = doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, detail)
	require.Contains(t, detail, "present and enabled")
	require.Empty(t, fix)

	tables, functions = hubObjectCounts(t, pool)
	wantTables, err := migrations.Tables()
	require.NoError(t, err)
	wantFunctions, err := migrations.Functions()
	require.NoError(t, err)
	require.Equal(t, len(wantTables), tables)
	require.Equal(t, len(wantFunctions), functions)
	requireEveryTriggerEnabled(t, pool)
	require.Equal(t, before, hubRowCounts(t, pool), "every table holds the backed-up rows")
	var epoch int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT restore_epoch FROM sync_hub_state`).Scan(&epoch))
	require.Equal(t, int64(7), epoch, "the restore epoch is the backed-up one")
}
