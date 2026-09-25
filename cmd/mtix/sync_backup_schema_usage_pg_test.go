// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// scramVerifier returns the SCRAM-SHA-256 verifier of password, the form
// in which a role password is sent to the server so the plaintext never
// reaches it (directive SQL Rule 1a).
func scramVerifier(t *testing.T, password string) string {
	t.Helper()
	const iterations = 4096
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	require.NoError(t, err)
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	require.NoError(t, err)
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		_, err := h.Write([]byte(msg))
		require.NoError(t, err)
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$4096:" + b64(salt) + "$" + b64(stored[:]) + ":" + b64(mac(salted, "Server Key"))
}

// loginRoleDSN makes role a login role with a random password and returns
// the fixture database's DSN for it.
func loginRoleDSN(t *testing.T, f *hardenFixture, role string) string {
	t.Helper()
	password := hardenRandomHex(t, 12)
	f.ddl("ALTER ROLE %I LOGIN PASSWORD %L", role, scramVerifier(t, password))
	u := f.dbURL
	u.User = url.UserPassword(role, password)
	return u.String()
}

// TestBackup_DSNRoleWithoutSchemaUsage_PrintsGrantsThatFixIt: the hub's
// tables are in schema hub_data, which another role owns, and the role the
// DSN names has hub_data on its search_path but no USAGE on it, so pg_dump
// sees no hub table. The backup fails, leaves no file and prints the
// grants: the schema's owner grants USAGE on the schema (the table owner
// cannot), and the table owner grants SELECT on each sync table and
// sync-table sequence by name, every sequence of the migrated hub. Run as
// printed by those roles, with the schema filled in, they let the backup
// dump every hub table, and give no access to another table of the owner
// in that schema (MTIX-95.1.4).
func TestBackup_DSNRoleWithoutSchemaUsage_PrintsGrantsThatFixIt(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	requirePgDumpForServer(t, f.dbURL.String())
	owner, schemaOwner := f.ownerRole(), f.role("schema_owner")
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", "hub_data", schemaOwner)
	f.ddl("GRANT USAGE, CREATE ON SCHEMA %I TO %I", "hub_data", owner)
	u := f.dbURL
	q := u.Query()
	q.Set("options", "-c role="+owner+" -c search_path=hub_data")
	u.RawQuery = q.Encode()
	t.Setenv(transport.EnvDSN, u.String())
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(context.Background(), &stdout, &stderr, nil, cloudOpts), stderr.String())
	sequences := f.strings(`SELECT s.relname::text FROM pg_catalog.pg_class s
		JOIN pg_catalog.pg_namespace n ON n.oid = s.relnamespace
		WHERE n.nspname = 'hub_data' AND s.relkind = 'S' ORDER BY 1`)
	require.NotEmpty(t, sequences)

	f.ddlAs(owner, "CREATE TABLE hub_data.app_notes (id integer PRIMARY KEY)")
	reader := f.role("reader")
	dsn := loginRoleDSN(t, f, reader)
	f.ddl("ALTER ROLE %I IN DATABASE %I SET search_path = hub_data, public", reader, f.dbName)
	out := filepath.Join(t.TempDir(), "hub.sql")
	_, errText, err := runBackupCLI(t, dsn, out, "--insecure-tls")
	require.Error(t, err, "without USAGE on hub_data pg_dump sees no hub table")
	require.Contains(t, errText, "no matching tables were found")
	require.NoFileExists(t, out)
	require.Contains(t, err.Error(), "GRANT SELECT ON SEQUENCE <schema>."+strings.Join(sequences, ", <schema>.")+
		` TO "`+reader+`"`, "the sequence grant names every sequence of the hub")
	usage := regexp.MustCompile(`the schema's owner runs (GRANT USAGE ON SCHEMA <schema> TO "[^"]+")`).
		FindStringSubmatch(err.Error())
	require.Len(t, usage, 2, "the schema's owner grants USAGE: %s", err.Error())
	selects := regexp.MustCompile(`GRANT SELECT ON [^;]+? TO "[^"]+"`).FindAllString(err.Error(), -1)
	require.Len(t, selects, 2, "the table owner grants SELECT on the tables and on the sequences: %s", err.Error())
	usageOnHub := `SELECT pg_catalog.has_schema_privilege($1, 'hub_data', 'USAGE')::text`
	f.ddlAs(owner, strings.ReplaceAll(usage[1], "<schema>", "hub_data"))
	require.Equal(t, []string{"false"}, f.strings(usageOnHub, reader), "the table owner cannot grant USAGE on the schema")
	f.ddlAs(schemaOwner, strings.ReplaceAll(usage[1], "<schema>", "hub_data"))
	require.Equal(t, []string{"true"}, f.strings(usageOnHub, reader), "the schema's owner can")
	for _, g := range selects {
		f.ddlAs(owner, strings.ReplaceAll(g, "<schema>", "hub_data"))
	}

	out = filepath.Join(t.TempDir(), "hub.sql")
	_, errText, err = runBackupCLI(t, dsn, out, "--insecure-tls")
	require.NoError(t, err, errText)
	body, err := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	tables, err := migrations.Tables()
	require.NoError(t, err)
	require.Equal(t, tables, dumpedTables(string(body)), "the dump creates every hub table")
	require.Equal(t, []string{"false"}, f.strings(`SELECT pg_catalog.has_table_privilege($1, 'hub_data.app_notes', 'SELECT')::text`,
		reader), "the grants name the sync tables, not every table in the schema")
}
