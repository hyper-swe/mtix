// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// hardenFixture is one throwaway database plus throwaway roles for one
// `mtix sync harden` test (MTIX-95.1). Grants, default privileges and event
// triggers live in the database, so dropping it isolates them from every
// other PG suite. Roles are cluster-wide: fixture roles carry a random
// prefix, and the data-API roles are created only when absent and dropped
// only when this fixture created them. Every statement that names a
// variable identifier is built by format() on the server (SQL Rule 1a).
type hardenFixture struct {
	t         *testing.T
	dbURL     url.URL
	admin     *pgxpool.Pool
	prefix    string
	superuser string
	dbName    string
	madeRoles []string // cluster-wide roles without the prefix, made here
}

// newHardenFixture creates the fixture database, or skips when the test DSN
// is unset, is not a URL, or is not a superuser. Hosted roles usually cannot
// create roles, databases or event triggers, so these tests run only against
// a throwaway server (documented absence on the cloud gate).
func newHardenFixture(t *testing.T) *hardenFixture {
	t.Helper()
	dsn := os.Getenv(envCmdPGTestDSN)
	if dsn == "" {
		t.Skipf("set %s to enable the mtix sync harden tests", envCmdPGTestDSN)
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Skip("mtix sync harden fixtures need a URL-form MTIX_PG_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	f := &hardenFixture{t: t, prefix: "mtixt_" + hardenRandomHex(t, 4)}
	var super bool
	require.NoError(t, root.QueryRow(ctx,
		`SELECT current_user::text, rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user`,
	).Scan(&f.superuser, &super))
	if !super {
		root.Close()
		t.Skip("mtix sync harden fixtures create roles, databases and event triggers; " +
			"they need a superuser test DSN on a throwaway server")
	}
	dbName := "mtix_h_" + hardenRandomHex(t, 6)
	hardenDDL(t, root, "", "CREATE DATABASE %I", dbName)
	f.dbURL, f.dbName = *u, dbName
	f.dbURL.Path = "/" + dbName
	f.admin, err = pgxpool.New(ctx, f.dbURL.String())
	require.NoError(t, err)
	t.Setenv("MTIX_SYNC_HOOK", "")
	t.Cleanup(func() { f.cleanup(root, dbName) })
	return f
}

// cleanup drops the fixture database, then every role the fixture made in
// one statement: a role that granted a membership to another can be dropped
// only together with it.
func (f *hardenFixture) cleanup(root *pgxpool.Pool, dbName string) {
	defer root.Close()
	f.admin.Close()
	hardenDDL(f.t, root, "", "DROP DATABASE IF EXISTS %I WITH (FORCE)", dbName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Fixture roles share the random prefix; made data-API roles are listed.
	var stmt *string
	require.NoError(f.t, root.QueryRow(ctx, `
		SELECT CASE WHEN count(*) > 0
		       THEN format('DROP ROLE IF EXISTS %s', string_agg(quote_ident(r.rolname), ', ')) END
		FROM pg_catalog.pg_roles r
		WHERE starts_with(r.rolname, $1) OR r.rolname = ANY($2::text[])`,
		f.prefix+"_", f.madeRoles).Scan(&stmt))
	if stmt != nil {
		_, err := root.Exec(ctx, *stmt)
		require.NoErrorf(f.t, err, "fixture: %s", *stmt)
	}
}

// role creates a NOLOGIN role named <prefix>_<suffix> and returns its name.
func (f *hardenFixture) role(suffix string) string {
	f.t.Helper()
	name := f.prefix + "_" + suffix
	f.ddl("CREATE ROLE %I NOLOGIN", name)
	return name
}

// ownerRole creates the role that runs `mtix sync init` and so owns the
// sync tables.
func (f *hardenFixture) ownerRole() string {
	f.t.Helper()
	owner := f.role("owner")
	f.ddl("GRANT USAGE, CREATE ON SCHEMA public TO %I", owner)
	return owner
}

// dataAPIRoles makes sure the anonymous and signed-in data-API roles exist
// and returns their names; a role this call creates is dropped at cleanup.
func (f *hardenFixture) dataAPIRoles() (string, string) {
	f.t.Helper()
	for _, name := range []string{"anon", "authenticated"} {
		var exists bool
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		require.NoError(f.t, f.admin.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)`, name).Scan(&exists))
		cancel()
		if !exists {
			f.ddl("CREATE ROLE %I NOLOGIN", name)
			f.madeRoles = append(f.madeRoles, name)
		}
	}
	return "anon", "authenticated"
}

// dsnAs returns the fixture database DSN with the session role set to role
// at connect time: current_user is role and superuser is dropped.
func (f *hardenFixture) dsnAs(role string) string {
	u := f.dbURL
	q := u.Query()
	q.Set("options", "-c role="+role)
	u.RawQuery = q.Encode()
	return u.String()
}

// migrateAs runs the hub migrations acting as role, after asserting the
// session really is that non-superuser role.
func (f *hardenFixture) migrateAs(role string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, f.dsnAs(role), transport.Options{InsecureTLS: true})
	require.NoError(f.t, err)
	defer pool.Close()
	var who string
	var super bool
	require.NoError(f.t, pool.Inner().QueryRow(ctx,
		`SELECT current_user::text, r.rolsuper FROM pg_catalog.pg_roles r WHERE r.rolname = current_user`,
	).Scan(&who, &super))
	require.Equal(f.t, role, who)
	require.False(f.t, super)
	require.NoError(f.t, pool.Migrate(ctx))
}

// exec runs a constant statement as the superuser on the fixture database.
func (f *hardenFixture) exec(sql string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := f.admin.Exec(ctx, sql)
	require.NoErrorf(f.t, err, "fixture: %s", sql)
}

// ddl builds template with format(%I ...) on the server and runs it as the
// superuser on the fixture database.
func (f *hardenFixture) ddl(template string, idents ...string) {
	f.t.Helper()
	hardenDDL(f.t, f.admin, "", template, idents...)
}

// ddlAs is ddl run with the transaction's role set to role.
func (f *hardenFixture) ddlAs(role, template string, idents ...string) {
	f.t.Helper()
	hardenDDL(f.t, f.admin, role, template, idents...)
}

// tryAs runs a constant statement in a transaction acting as role and
// returns its error.
func (f *hardenFixture) tryAs(role, sql string) error {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := f.admin.Begin(ctx)
	require.NoError(f.t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT set_config('role', $1, true)`, role)
	require.NoError(f.t, err)
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// hardenDDL builds template with format() server-side and runs the result,
// acting as role inside a transaction when role is set.
func hardenDDL(t *testing.T, pool *pgxpool.Pool, role, template string, idents ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stmt string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT format($1::text, VARIADIC $2::text[])`, template, idents).Scan(&stmt))
	if role == "" {
		_, err := pool.Exec(ctx, stmt)
		require.NoErrorf(t, err, "fixture: %s", stmt)
		return
	}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT set_config('role', $1, true)`, role)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, stmt)
	require.NoErrorf(t, err, "fixture as %s: %s", role, stmt)
	require.NoError(t, tx.Commit(ctx))
}

// strings runs a query as the superuser and returns its single text column.
func (f *hardenFixture) strings(sql string, args ...any) []string {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := f.admin.Query(ctx, sql, args...)
	require.NoError(f.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(f.t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(f.t, rows.Err())
	return out
}

// snapshot captures every privilege, default privilege, trigger state and
// fixture-role membership a harden run could change, as sorted text.
func (f *hardenFixture) snapshot() []string {
	f.t.Helper()
	return f.strings(`
		SELECT 'rel ' || c.oid::regclass::text || ' ' || COALESCE(c.relacl::text, '-')
		FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		UNION ALL
		SELECT 'col ' || a.attrelid::regclass::text || '.' || a.attname::text || ' ' || a.attacl::text
		FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND a.attacl IS NOT NULL
		UNION ALL
		SELECT 'fn ' || p.oid::regprocedure::text || ' ' || COALESCE(p.proacl::text, '-')
		FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = 'public'
		UNION ALL
		SELECT 'def ' || d.defaclrole::regrole::text || ' ' || d.defaclnamespace::text || ' '
		       || d.defaclobjtype::text || ' ' || d.defaclacl::text
		FROM pg_catalog.pg_default_acl d
		UNION ALL
		SELECT 'trg ' || t.tgrelid::regclass::text || ' ' || t.tgname::text || ' ' || t.tgenabled::text
		FROM pg_catalog.pg_trigger t WHERE NOT t.tgisinternal
		UNION ALL
		SELECT 'mem ' || m.roleid::regrole::text || ' ' || m.member::regrole::text
		FROM pg_catalog.pg_auth_members m
		WHERE starts_with(m.member::regrole::text, $1)
		ORDER BY 1`, f.prefix+"_")
}

// privileges lists every privilege role holds on any sync table, sequence
// or mtix function in the fixture database, as "object privilege"; a
// privilege held on some columns only reads "object column privilege".
func (f *hardenFixture) privileges(role string) []string {
	f.t.Helper()
	return f.strings(`
		SELECT c.relname || ' ' || p.priv
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE','TRUNCATE','REFERENCES','TRIGGER']) AS p(priv)
		WHERE n.nspname = 'public' AND c.relkind = 'r'
		  AND pg_catalog.has_table_privilege($1::text, c.oid, p.priv)
		UNION ALL
		SELECT c.relname || ' column ' || p.priv
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN unnest(ARRAY['SELECT','INSERT','UPDATE','REFERENCES']) AS p(priv)
		WHERE n.nspname = 'public' AND c.relkind = 'r'
		  AND NOT pg_catalog.has_table_privilege($1::text, c.oid, p.priv)
		  AND pg_catalog.has_any_column_privilege($1::text, c.oid, p.priv)
		UNION ALL
		SELECT c.relname || ' ' || p.priv
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN unnest(ARRAY['USAGE','SELECT','UPDATE']) AS p(priv)
		WHERE n.nspname = 'public' AND c.relkind = 'S'
		  AND pg_catalog.has_sequence_privilege($1::text, c.oid, p.priv)
		UNION ALL
		SELECT pr.proname || ' EXECUTE'
		FROM pg_catalog.pg_proc pr JOIN pg_catalog.pg_namespace n ON n.oid = pr.pronamespace
		WHERE n.nspname = 'public' AND pg_catalog.has_function_privilege($1::text, pr.oid, 'EXECUTE')
		ORDER BY 1`, role)
}

// harden runs `mtix sync harden --insecure-tls <args>` through the sync
// command tree, connected as role, and returns stdout and the error.
func (f *hardenFixture) harden(role string, args ...string) (string, error) {
	f.t.Helper()
	f.t.Setenv(transport.EnvDSN, f.dsnAs(role))
	cmd := newSyncCmd()
	// As under the root command, which silences cobra's own error and
	// usage output: the command prints its report itself.
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"harden", "--insecure-tls"}, args...))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	return stdout.String(), err
}

// hardenRandomHex returns n random bytes as lowercase hex.
func hardenRandomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}
