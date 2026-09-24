// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// livePoolForms returns the DSN forms the live-pool test opens: the test
// DSN as configured and, when it names one authority host, the same
// server listed twice (a fallback entry) and, on a loopback host, the
// same server under sslmode=prefer (a TLS entry and a plain entry).
func livePoolForms(t *testing.T, dsn string) map[string]string {
	t.Helper()
	forms := map[string]string{"as configured": dsn}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" || strings.Contains(u.Host, ",") {
		return forms
	}
	twice := *u
	twice.Host = u.Host + "," + u.Host
	forms["same server listed twice"] = twice.String()
	if isLoopbackHost(u.Hostname()) {
		prefer := *u
		q := prefer.Query()
		q.Set("sslmode", "prefer")
		prefer.RawQuery = q.Encode()
		forms["loopback server under prefer"] = prefer.String()
	}
	return forms
}

// TestNew_LivePool_ConfigMatchesApprovedConfig opens a real pool and
// pins that its configuration has the same hosts, ports and per-host
// TLS settings as the configuration ApproveDSN approves for the same
// DSN (FR-18.15, MTIX-95.25). Skips unless MTIX_PG_TEST_DSN is set.
func TestNew_LivePool_ConfigMatchesApprovedConfig(t *testing.T) {
	dsn := requireTestDSN(t)
	opts := transport.Options{InsecureTLS: true}
	for name, form := range livePoolForms(t, dsn) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool, err := transport.New(ctx, form, opts)
			require.NoError(t, err)
			defer pool.Close()

			a, err := transport.ApproveDSN(form, opts)
			require.NoError(t, err)
			want := entriesOf(&a.Config.ConnConfig.Config)
			got := entriesOf(&pool.Inner().Config().ConnConfig.Config)
			requireSameEntries(t, want, got)
			require.Equal(t, transport.DefaultPoolDefaults().MaxConns, pool.Inner().Config().MaxConns)
		})
	}
}
