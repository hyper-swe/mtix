// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Fixture DSNs preserve transport settings while targeting their owned database.
func TestCmdPGDatabaseDSN_URIAndKeywordFormsTargetOwnedDatabase(t *testing.T) {
	const name = "mtix_cmd_0123456789abcdef0123456789abcdef"
	for _, tt := range []struct {
		name, dsn string
	}{
		{"URI", "postgres://fixture@127.0.0.1:5433/shared?sslmode=disable&application_name=fixture"},
		{"URI dbname override", "postgres://fixture@127.0.0.1:5433/shared?sslmode=disable&application_name=fixture&dbname=shared"},
		{"URI database override", "postgres://fixture@127.0.0.1:5433/shared?sslmode=disable&application_name=fixture&database=shared"},
		{"keyword", "host=127.0.0.1 port=5433 user=fixture dbname=shared sslmode=disable application_name=fixture"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(cmdPGDatabaseDSN(t, tt.dsn, name))
			require.NoError(t, err)
			require.Equal(t, name, cfg.ConnConfig.Database)
			require.Equal(t, "127.0.0.1", cfg.ConnConfig.Host)
			require.EqualValues(t, 5433, cfg.ConnConfig.Port)
			require.Equal(t, "fixture", cfg.ConnConfig.User)
			require.Equal(t, "fixture", cfg.ConnConfig.RuntimeParams["application_name"])
			require.Nil(t, cfg.ConnConfig.TLSConfig)
		})
	}
}
