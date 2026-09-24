// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestNewWithDefaults_DSNForms_PoolConfigIsTheApprovedConfig pins, for
// every DSN form the posture tests use (host lists, fallbacks, PG*
// environment, service file, CA from MTIX_SYNC_SSLROOTCERT, weak modes on
// loopback), that the configuration NewWithDefaults hands to pgxpool is
// the approved configuration itself, with the same hosts, ports and
// per-host TLS settings the posture step evaluated. Only pool settings
// are applied to it (FR-18.15, MTIX-95.25).
func TestNewWithDefaults_DSNForms_PoolConfigIsTheApprovedConfig(t *testing.T) {
	caPath, _ := writeTestCA(t)
	defs := transport.DefaultPoolDefaults()
	forms := append(verifyFullForms(), weakForms()...)
	for _, f := range forms {
		t.Run(formName(f), func(t *testing.T) {
			dsn, opts := applyPostureForm(t, f, caPath)
			a, err := transport.ApproveDSN(dsn, opts)
			require.NoError(t, err)
			approved := entriesOf(&a.Config.ConnConfig.Config)
			require.Equal(t, f.wantHosts, hostList(approved))

			cfg := transport.PoolConfigForTest(a, defs)
			require.Same(t, a.Config, cfg, "the pool is built from the approved configuration itself")
			requireSameEntries(t, approved, entriesOf(&cfg.ConnConfig.Config))
			require.Equal(t, defs.MaxConns, cfg.MaxConns)
			require.Equal(t, defs.ConnLifetime, cfg.MaxConnLifetime)
			require.Equal(t, defs.HealthCheckPeriod, cfg.HealthCheckPeriod)
			require.NotNil(t, cfg.AfterConnect, "statement_timeout is applied after connect")
		})
	}
}

// TestNewWithDefaults_LocalHostCases_PoolConfigIsTheApprovedConfig runs
// the same check over every form the loopback posture test accepts,
// under every weak sslmode (FR-18.15, MTIX-95.25).
func TestNewWithDefaults_LocalHostCases_PoolConfigIsTheApprovedConfig(t *testing.T) {
	defs := transport.DefaultPoolDefaults()
	for _, tc := range localHostCases() {
		for _, mode := range weakModes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				dsn := withSSLMode(applyHostCase(t, tc), mode)
				a, err := transport.ApproveDSN(dsn, transport.Options{InsecureTLS: true})
				require.NoError(t, err)
				approved := entriesOf(&a.Config.ConnConfig.Config)
				for i, e := range approved {
					require.True(t, isSocketHost(e.Host) || isLoopbackHost(e.Host),
						"entry %d is neither loopback nor a local socket", i)
				}
				cfg := transport.PoolConfigForTest(a, defs)
				require.Same(t, a.Config, cfg, "the pool is built from the approved configuration itself")
				requireSameEntries(t, approved, entriesOf(&cfg.ConnConfig.Config))
			})
		}
	}
}

// TestNewWithDefaults_ZeroStatementTimeout_LeavesAfterConnectUnset pins
// that a zero StatementTimeout opts out of the per-connection SET.
func TestNewWithDefaults_ZeroStatementTimeout_LeavesAfterConnectUnset(t *testing.T) {
	pinPGEnv(t)
	a, err := transport.ApproveDSN("postgres://u:pw@db.example.com/hub", transport.Options{})
	require.NoError(t, err)
	defs := transport.DefaultPoolDefaults()
	defs.StatementTimeout = 0
	cfg := transport.PoolConfigForTest(a, defs)
	require.Nil(t, cfg.AfterConnect)
}
