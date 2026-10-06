// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These PG-gated tests verify SQL Rule 1a's bound session setting on
// distinct backend connections, reuse, commit, and server cancellation.
func settingTestPool(t *testing.T, timeout time.Duration) *Pool {
	t.Helper()
	dsn := os.Getenv("MTIX_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set MTIX_PG_TEST_DSN for session setting proof")
	}
	defs := DefaultPoolDefaults()
	defs.MaxConns = 2
	defs.StatementTimeout = timeout
	p, err := NewWithDefaults(context.Background(), dsn, Options{InsecureTLS: true}, defs)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

func assertSessionTimeout(t *testing.T, conn *pgxpool.Conn, milliseconds int64) {
	t.Helper()
	var displayed string
	require.NoError(t, conn.QueryRow(context.Background(), `SHOW statement_timeout`).Scan(&displayed))
	require.NotEmpty(t, displayed)
	var setting int64
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT setting::bigint FROM pg_settings WHERE name = 'statement_timeout'`).Scan(&setting))
	require.Equal(t, milliseconds, setting)
}

func TestStatementTimeout_NewReusedAndCommittedConnections(t *testing.T) {
	tests := []struct {
		name         string
		timeout      time.Duration
		milliseconds int64
	}{
		{"positive", 1250 * time.Millisecond, 1250},
		{"fractional", 1250500 * time.Microsecond, 1250},
		{"submillisecond", 500 * time.Microsecond, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := settingTestPool(t, tt.timeout)
			a, err := p.p.Acquire(context.Background())
			require.NoError(t, err)
			defer a.Release()
			b, err := p.p.Acquire(context.Background())
			require.NoError(t, err)
			defer b.Release()
			require.NotEqual(t, a.Conn().PgConn().PID(), b.Conn().PgConn().PID())
			assertSessionTimeout(t, a, tt.milliseconds)
			assertSessionTimeout(t, b, tt.milliseconds)
			tx, err := b.Begin(context.Background())
			require.NoError(t, err)
			require.NoError(t, tx.Commit(context.Background()))
			assertSessionTimeout(t, b, tt.milliseconds)
			pid := b.Conn().PgConn().PID()
			b.Release()
			reused, err := p.p.Acquire(context.Background())
			require.NoError(t, err)
			defer reused.Release()
			require.Equal(t, pid, reused.Conn().PgConn().PID())
			assertSessionTimeout(t, reused, tt.milliseconds)
		})
	}
}

func TestStatementTimeout_ServerCancelsSlowQuery(t *testing.T) {
	p := settingTestPool(t, 100*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.p.Exec(ctx, `SELECT pg_sleep($1)`, 2)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "server timeout must produce a PostgreSQL error: %v", err)
	require.Equal(t, "57014", pgErr.Code)
	require.Contains(t, pgErr.Message, "statement timeout")
}

func TestStatementTimeout_DisabledDoesNotInstallHook(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			approval, err := ApproveDSN("postgres://user@127.0.0.1/db?sslmode=disable", Options{InsecureTLS: true})
			require.NoError(t, err)
			defs := DefaultPoolDefaults()
			defs.StatementTimeout = timeout
			require.Nil(t, poolConfig(approval, defs).AfterConnect)
		})
	}
}
