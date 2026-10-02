// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestApproveDSN_LibpqSettings_ReportCAFileAndTargetSessionAttrs: next
// to its parsed Config, an Approval reports the settings a libpq client
// such as pg_dump needs that the parsed Config does not keep: the CA file
// the normalized DSN names and the DSN's target_session_attrs. Printing
// the Approval still shows neither (FR-18.15, MTIX-95.7).
func TestApproveDSN_LibpqSettings_ReportCAFileAndTargetSessionAttrs(t *testing.T) {
	caPath, _ := writeTestCA(t)
	otherCA, _ := writeTestCA(t)
	const base = "postgres://u:pw@db.example.com/hub"
	tests := []struct {
		name    string
		dsn     string
		env     map[string]string
		wantCA  string
		wantTSA string
	}{
		{"neither named", base, nil, "", ""},
		{"CA named in the DSN", base + "?sslrootcert=" + url.QueryEscape(caPath), nil, caPath, ""},
		{"CA from MTIX_SYNC_SSLROOTCERT", base, map[string]string{transport.EnvSSLRootCert: caPath}, caPath, ""},
		{"the DSN's CA wins over MTIX_SYNC_SSLROOTCERT", base + "?sslrootcert=" + url.QueryEscape(otherCA),
			map[string]string{transport.EnvSSLRootCert: caPath}, otherCA, ""},
		{"target_session_attrs named in the DSN", base + "?target_session_attrs=read-write", nil, "", "read-write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			a, err := transport.ApproveDSN(tt.dsn, transport.Options{})
			require.NoError(t, err)
			require.Equal(t, tt.wantCA, a.SSLRootCert)
			require.Equal(t, tt.wantTSA, a.TargetSessionAttrs)
			printed := fmt.Sprintf("%v %+v %#v %s", a, a, a, *a)
			if tt.wantCA != "" {
				require.NotContains(t, printed, tt.wantCA, "printing an Approval names no path")
			}
		})
	}
}
