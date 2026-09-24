// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// hubFixture is one throwaway database plus throwaway roles for one test
// (MTIX-95.1). Grants, default privileges and event triggers live in the
// database, so dropping it keeps them away from every other PG suite.
// Roles are cluster-wide, so each carries a random prefix and is dropped
// at cleanup. Every statement that names a variable identifier is built
// by format() on the server (SQL Rule 1a).
type hubFixture struct {
	t      *testing.T
	dbURL  url.URL       // superuser URL of the fixture database
	admin  *pgxpool.Pool // superuser pool on the fixture database
	prefix string
}

// newHubFixture creates the fixture database, or skips when the test DSN
// is unset, is not a URL, or is not a superuser. Hosted roles usually
// cannot create roles, databases or event triggers, so these tests run
// only against a throwaway container (documented absence on the cloud gate).
func newHubFixture(t *testing.T) *hubFixture {
	t.Helper()
	dsn := requireTestDSN(t)
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Skip("hub privilege fixtures need a URL-form MTIX_PG_TEST_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	var super bool
	require.NoError(t, root.QueryRow(ctx,
		`SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user`).Scan(&super))
	if !super {
		root.Close()
		t.Skip("hub privilege fixtures create roles, databases and event triggers; " +
			"they need a superuser test DSN on a throwaway server")
	}
	f := &hubFixture{t: t, prefix: "mtixt_" + randomHex(t, 4)}
	dbName := "mtix_h_" + randomHex(t, 6)
	fixtureDDL(t, root, "CREATE DATABASE %I", dbName)
	f.dbURL = *u
	f.dbURL.Path = "/" + dbName
	f.admin, err = pgxpool.New(ctx, f.dbURL.String())
	require.NoError(t, err)
	t.Cleanup(func() { f.cleanup(root, dbName) })
	return f
}

// cleanup drops the fixture database, then every role the fixture created
// in one statement, so roles that depend on each other drop together.
func (f *hubFixture) cleanup(root *pgxpool.Pool, dbName string) {
	defer root.Close()
	f.admin.Close()
	fixtureDDL(f.t, root, "DROP DATABASE IF EXISTS %I WITH (FORCE)", dbName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Roles this fixture created share its random prefix.
	var stmt *string
	require.NoError(f.t, root.QueryRow(ctx, `
		SELECT format('DROP ROLE IF EXISTS %s', string_agg(quote_ident(r.rolname), ', '))
		FROM pg_catalog.pg_roles r WHERE starts_with(r.rolname, $1)`, f.prefix+"_").Scan(&stmt))
	if stmt != nil {
		_, err := root.Exec(ctx, *stmt)
		require.NoErrorf(f.t, err, "fixture: %s", *stmt)
	}
}

// role creates a NOLOGIN role named <prefix>_<suffix> and returns its name.
// Tests act as it through dsnAs, which needs no password.
func (f *hubFixture) role(suffix string) string {
	f.t.Helper()
	name := f.prefix + "_" + suffix
	f.ddl("CREATE ROLE %I NOLOGIN", name)
	return name
}

// ownerRole creates a role that may create objects in the public schema,
// as the role that runs `mtix sync init` on a real hub does.
func (f *hubFixture) ownerRole() string {
	f.t.Helper()
	owner := f.role("owner")
	f.ddl("GRANT USAGE, CREATE ON SCHEMA public TO %I", owner)
	return owner
}

// dsnAs returns the fixture database DSN with the session role set to
// role at connect time, so current_user is role and superuser is dropped.
func (f *hubFixture) dsnAs(role string) string {
	u := f.dbURL
	q := u.Query()
	q.Set("options", "-c role="+role)
	u.RawQuery = q.Encode()
	return u.String()
}

// openAs opens a transport pool on the fixture database acting as role,
// and asserts the session really is that non-superuser role.
func (f *hubFixture) openAs(role string, opts transport.Options) *transport.Pool {
	f.t.Helper()
	opts.InsecureTLS = true
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, f.dsnAs(role), opts)
	require.NoError(f.t, err)
	f.t.Cleanup(pool.Close)
	var who string
	var super bool
	require.NoError(f.t, pool.Inner().QueryRow(ctx,
		`SELECT current_user, r.rolsuper FROM pg_catalog.pg_roles r WHERE r.rolname = current_user`,
	).Scan(&who, &super))
	require.Equal(f.t, role, who, "session must act as the fixture role")
	require.False(f.t, super, "the fixture role must not be a superuser")
	return pool
}

// exec runs a constant statement as the superuser on the fixture database.
func (f *hubFixture) exec(sql string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := f.admin.Exec(ctx, sql)
	require.NoErrorf(f.t, err, "fixture: %s", sql)
}

// ddl builds a statement from template and identifiers with format() on
// the server, then runs it as the superuser on the fixture database.
func (f *hubFixture) ddl(template string, idents ...string) {
	f.t.Helper()
	fixtureDDL(f.t, f.admin, template, idents...)
}

// fixtureDDL builds template with format(%I ...) server-side and runs it.
func fixtureDDL(t *testing.T, pool *pgxpool.Pool, template string, idents ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stmt string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT format($1::text, VARIADIC $2::text[])`, template, idents).Scan(&stmt))
	_, err := pool.Exec(ctx, stmt)
	require.NoErrorf(t, err, "fixture: %s", stmt)
}

// queryStrings runs a constant query with args on the fixture database as
// the superuser and returns its single text column.
func (f *hubFixture) queryStrings(sql string, args ...any) []string {
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

// randomHex returns n random bytes as lowercase hex.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}
