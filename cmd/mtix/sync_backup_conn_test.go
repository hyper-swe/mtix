// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-61: mtix sync backup parses the DSN with pgx (which tolerates forms
// pg_dump's libpq URI parser rejects, e.g. an unencoded special char in the
// password) and hands pg_dump the connection via PG* env vars instead of the
// raw DSN string.

func TestPgDumpConnParams_ExtractsComponents(t *testing.T) {
	isolateTrustEnv(t)
	c, err := pgDumpConnParams("postgres://alice:s3cret@db.example.com:6543/appdb?sslmode=verify-full&sslrootcert=/tmp/ca.pem")
	require.NoError(t, err)
	assert.Equal(t, "db.example.com", c.host)
	assert.Equal(t, "6543", c.port)
	assert.Equal(t, "alice", c.user)
	assert.Equal(t, "s3cret", c.password)
	assert.Equal(t, "appdb", c.database)
	assert.Equal(t, "verify-full", c.sslmode)
	assert.Equal(t, "/tmp/ca.pem", c.sslrootcert)
}

func TestPgDumpConnParams_SpecialCharPassword(t *testing.T) {
	isolateTrustEnv(t)
	// The '@' in the password (percent-encoded in the URI) is exactly what made
	// pg_dump's URI parser mis-split the host; pgx decodes it to a literal '@',
	// which then goes to PGPASSWORD verbatim.
	c, err := pgDumpConnParams("postgres://postgres.ref:p%40ss123@aws-1-region.pooler.supabase.com:5432/postgres?sslmode=verify-full&sslrootcert=/tmp/ca.pem")
	require.NoError(t, err)
	assert.Equal(t, "p@ss123", c.password)
	assert.Equal(t, "aws-1-region.pooler.supabase.com", c.host)
	assert.Equal(t, "postgres.ref", c.user)
	assert.Equal(t, "postgres", c.database)
}

func TestPgDumpConnParams_DefaultsSslrootcertSystem(t *testing.T) {
	isolateTrustEnv(t)
	c, err := pgDumpConnParams("postgres://u:pw@host:5432/db?sslmode=verify-full")
	require.NoError(t, err)
	assert.Equal(t, "verify-full", c.sslmode)
	assert.Equal(t, "system", c.sslrootcert,
		"verify-full with no cert defaults to the OS trust store (MTIX-59)")
}
