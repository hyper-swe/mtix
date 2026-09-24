// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// MTIX-61: mtix sync backup hands pg_dump the connection via PG* env vars
// instead of the raw DSN string. MTIX-95.7: the connection comes from the
// configuration the sync transport approves (transport.ApproveDSN), which
// tolerates DSN forms pg_dump's libpq URI parser rejects, such as a
// percent-encoded special character in the password.

// writeBackupTestCA writes a self-signed CA certificate to a PEM file and
// returns its path.
func writeBackupTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mtix backup test CA"},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return p
}

func TestPgDumpConnParams_ExtractsComponents(t *testing.T) {
	isolateTrustEnv(t)
	ca := writeBackupTestCA(t)
	c, err := pgDumpConnParams("postgres://alice:s3cret@db.example.com:6543/appdb?sslmode=verify-full&sslrootcert="+
		url.QueryEscape(ca), transport.Options{})
	require.NoError(t, err)
	assert.Equal(t, "db.example.com", c.host)
	assert.Equal(t, "6543", c.port)
	assert.Equal(t, "alice", c.user)
	assert.Equal(t, "s3cret", c.password)
	assert.Equal(t, "appdb", c.database)
	assert.Equal(t, "verify-full", c.sslmode)
	assert.Equal(t, ca, c.sslrootcert)
}

func TestPgDumpConnParams_SpecialCharPassword(t *testing.T) {
	isolateTrustEnv(t)
	// The '@' in the password (percent-encoded in the URI) is exactly what made
	// pg_dump's URI parser mis-split the host; pgx decodes it to a literal '@',
	// which then goes to PGPASSWORD verbatim.
	c, err := pgDumpConnParams("postgres://postgres.ref:p%40ss123@pooler.example.com:5432/postgres?sslmode=verify-full",
		transport.Options{})
	require.NoError(t, err)
	assert.Equal(t, "p@ss123", c.password)
	assert.Equal(t, "pooler.example.com", c.host)
	assert.Equal(t, "postgres.ref", c.user)
	assert.Equal(t, "postgres", c.database)
}

func TestPgDumpConnParams_DefaultsSslrootcertSystem(t *testing.T) {
	isolateTrustEnv(t)
	c, err := pgDumpConnParams("postgres://u:pw@host:5432/db?sslmode=verify-full", transport.Options{})
	require.NoError(t, err)
	assert.Equal(t, "verify-full", c.sslmode)
	assert.Equal(t, "system", c.sslrootcert,
		"verify-full with no cert defaults to the OS trust store (MTIX-59)")
}

// TestPgDumpConnParams_HostList_OneEntryPerHostInDialOrder: the PGHOST and
// PGPORT lists name each host of the approved configuration once, in dial
// order, including under prefer, where the driver tries each host twice
// (MTIX-95.7).
func TestPgDumpConnParams_HostList_OneEntryPerHostInDialOrder(t *testing.T) {
	tests := []struct {
		name      string
		dsn       string
		opts      transport.Options
		wantHosts string
		wantPorts string
	}{
		{"one host", "postgres://u:pw@db.example.com/hub", transport.Options{}, "db.example.com", "5432"},
		{"two hosts", "postgres://u:pw@a.example.com:5433,b.example.com:5434/hub", transport.Options{},
			"a.example.com,b.example.com", "5433,5434"},
		{"prefer tries each loopback host twice", "postgres://u:pw@127.0.0.1:5433,localhost:5434/hub?sslmode=prefer",
			transport.Options{InsecureTLS: true}, "127.0.0.1,localhost", "5433,5434"},
		{"a local socket", "postgres://u:pw@/hub?host=/tmp&sslmode=disable", transport.Options{InsecureTLS: true},
			"/tmp", "5432"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateTrustEnv(t)
			c, err := pgDumpConnParams(tt.dsn, tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.wantHosts, c.host)
			assert.Equal(t, tt.wantPorts, c.port)
		})
	}
}
