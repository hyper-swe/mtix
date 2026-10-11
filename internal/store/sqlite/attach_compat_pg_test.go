// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachReaderPull supplies real remote events to the released reader on an isolated hub.
func attachReaderPull(t *testing.T, binary, project, path, dsn string) {
	t.Helper()
	pool, err := transport.New(t.Context(), dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Migrate(t.Context()))
	var version string
	require.NoError(t, pool.Inner().QueryRow(t.Context(), "SHOW server_version_num").Scan(&version))
	t.Logf("real PostgreSQL server_version_num=%s", version)
	producer, err := sqlite.New(filepath.Join(t.TempDir(), "producer.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, producer.Close()) })
	createPendingTestNode(t, producer, "TEST-900", 900)
	node, err := producer.GetNode(t.Context(), "TEST-900")
	require.NoError(t, err)
	events, err := producer.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	accepted, conflicts, err := pool.PushEvents(t.Context(), events)
	require.NoError(t, err)
	require.Empty(t, conflicts)
	require.Len(t, accepted, len(events))
	attachReaderCommand(t, binary, project, dsn, "sync", "pull", "--insecure-tls")
	target, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, target.Close()) })
	got, err := target.GetNode(t.Context(), "TEST-900")
	require.NoError(t, err)
	assert.Equal(t, node.UID, got.UID)
	assert.Equal(t, node.Title, got.Title)
	var applied int
	require.NoError(t, target.ReadDB().QueryRow("SELECT count(*) FROM applied_events WHERE event_id=?", events[0].EventID).Scan(&applied))
	require.Equal(t, 1, applied)
	t.Log("ATTACH_COMPAT_REMOTE_APPLIED")
}
