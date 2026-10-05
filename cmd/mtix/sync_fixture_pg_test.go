// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// cmdPGFixtureDSN keeps a unique database for the lifetime of each cmd test.
// Reset helpers and production migration calls then cannot contend with another
// test package or process using the same MTIX_PG_TEST_DSN (MTIX-107.67).
// The configured disposable server login must have CREATE DATABASE permission.
// t.Setenv scopes reuse to the test, avoiding shared maps or mutexes.
func cmdPGFixtureDSN(t *testing.T, baseDSN string) string {
	t.Helper()
	const fixtureEnv = "MTIX_CMD_PG_FIXTURE_DSN"
	if dsn := os.Getenv(fixtureEnv); dsn != "" {
		return dsn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, baseDSN)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	var random [16]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	name := "mtix_cmd_" + hex.EncodeToString(random[:])
	cmdPGDatabaseDDL(t, admin, "CREATE DATABASE %I", name)
	t.Cleanup(func() {
		cmdPGDatabaseDDL(t, admin, "DROP DATABASE %I WITH (FORCE)", name)
	})
	dsn := cmdPGDatabaseDSN(t, baseDSN, name)
	t.Setenv(fixtureEnv, dsn)
	return dsn
}

// cmdPGDatabaseDSN preserves both URI and keyword-form connection settings.
// URL encoding and a final keyword override preserve escaping; no DSN is logged.
func cmdPGDatabaseDSN(t *testing.T, dsn, name string) string {
	t.Helper()
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		u.Path = "/" + name
		u.RawPath = ""
		// URI query settings override the path in pgx; remove both database aliases.
		query := u.Query()
		query.Del("dbname")
		query.Del("database")
		u.RawQuery = query.Encode()
		return u.String()
	}
	// The generated identifier needs no keyword escaping. pgx accepts the last
	// occurrence of dbname; ConnString itself retains the original input string.
	return dsn + " dbname=" + name
}

// cmdPGDatabaseDDL builds identifiers server-side with bound arguments (SQL
// Rule 1a). Only the random, validated database name this fixture owns is used.
func cmdPGDatabaseDDL(t *testing.T, pool *pgxpool.Pool, template, name string) {
	t.Helper()
	valid, err := regexp.MatchString(`^mtix_cmd_[a-f0-9]{32}$`, name)
	require.NoError(t, err)
	require.True(t, valid, "fixture database identifier must be generated and validated")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var statement string
	// PostgreSQL quotes the database identifier; Go never interpolates SQL.
	err = pool.QueryRow(ctx, `SELECT format($1::text, $2::text)`, template, name).Scan(&statement)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, statement)
	require.NoError(t, err, "create/drop only the owned fixture database")
}
