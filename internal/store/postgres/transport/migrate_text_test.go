// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// Migration tracing proves the permitted embedded constant DDL is executed
// exactly once, without bound/runtime data or additional schema statements.
type migrationTraceEntry struct {
	sql  string
	args []any
}
type migrationTextTracer struct {
	mu      sync.Mutex
	entries []migrationTraceEntry
}

func (m *migrationTextTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, migrationTraceEntry{data.SQL, append([]any(nil), data.Args...)})
	return ctx
}

func (m *migrationTextTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func tracedMigrationPool(t *testing.T, tracer *migrationTextTracer) *Pool {
	t.Helper()
	dsn := os.Getenv("MTIX_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set MTIX_PG_TEST_DSN for migration text proof")
	}
	approval, err := ApproveDSN(dsn, Options{InsecureTLS: true})
	require.NoError(t, err)
	cfg := poolConfig(approval, DefaultPoolDefaults())
	cfg.MaxConns = 1
	cfg.ConnConfig.Tracer = tracer
	inner, err := openPool(context.Background(), cfg)
	require.NoError(t, err)
	p := &Pool{p: inner}
	t.Cleanup(p.Close)
	tracer.mu.Lock()
	tracer.entries = nil
	tracer.mu.Unlock()
	return p
}

func embeddedMigrationText(t *testing.T) string {
	t.Helper()
	files, err := migrations.Files()
	require.NoError(t, err)
	var bodies []string
	for _, name := range files {
		body, err := migrations.Read(name)
		require.NoError(t, err)
		bodies = append(bodies, body)
	}
	return strings.Join(bodies, "\n;\n") + "\n;\n"
}

func TestMigrate_ExactEmbeddedTextSingleSchemaExecution(t *testing.T) {
	tracer := &migrationTextTracer{}
	p := tracedMigrationPool(t, tracer)
	require.NoError(t, p.Migrate(context.Background()))
	tracer.mu.Lock()
	entries := append([]migrationTraceEntry(nil), tracer.entries...)
	tracer.mu.Unlock()
	// Count every query, not only exact matches: an extra or chunked DDL
	// execution changes this sequence and must fail the one-round-trip guard.
	require.Len(t, entries, 6)
	require.Equal(t, "begin", strings.ToLower(entries[0].sql))
	require.Equal(t, "SET LOCAL statement_timeout = 0", entries[1].sql)
	require.Equal(t, "SELECT pg_advisory_xact_lock(hashtext($1))", entries[2].sql)
	require.Equal(t, []any{AdvisoryLockKey}, entries[2].args)
	require.Contains(t, entries[3].sql, "FROM pg_catalog.pg_class p")
	require.Equal(t, embeddedMigrationText(t), entries[4].sql)
	require.Empty(t, entries[4].args)
	require.Equal(t, "commit", strings.ToLower(entries[5].sql))
}
