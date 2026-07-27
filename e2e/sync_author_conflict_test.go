// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-24: a distinct author identity per same-machine agent makes their
// concurrent same-field edits vector-clock-Concurrent, so the hub records a
// sync_conflicts row. Without it (both default to 'cli') the edits are VC-Equal
// and the hub logs nothing — the audit gap this ticket closes.

// hubConflictCount reads the hub's sync_conflicts row count. InsecureSkipVerify
// mirrors the transport path so it works against a private-CA pooler too.
func hubConflictCount(t *testing.T, dsn string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	if cfg.ConnConfig.TLSConfig != nil {
		cfg.ConnConfig.TLSConfig.InsecureSkipVerify = true //nolint:gosec // test-only hub read
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM sync_conflicts`).Scan(&n))
	return n
}

func TestE2E_AuthorID_DistinctAuthorsRecordHubConflict(t *testing.T) {
	dsn := requireSyncE2EDSN(t)
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	require.NoError(t, a.store.SetDefaultAuthor(ctx, "agent-a"))
	require.NoError(t, b.store.SetDefaultAuthor(ctx, "agent-b"))

	a.createNode(t, "PRJ-1", "initial")
	a.pushAll(ctx, t, pool)
	require.Equal(t, 1, b.pullAll(ctx, t, pool))

	// Concurrent edits to the same field by distinct authors.
	a.updateTitle(t, "PRJ-1", "from-A")
	b.updateTitle(t, "PRJ-1", "from-B")
	a.pushAll(ctx, t, pool)
	_, bConflicts := b.pushAll(ctx, t, pool)

	assert.Positive(t, bConflicts,
		"distinct authors → vector-clock-Concurrent edits → the hub records a conflict")
	assert.Positive(t, hubConflictCount(t, dsn), "hub sync_conflicts must carry the row")
}

func TestE2E_AuthorID_SharedDefaultRecordsNoConflict(t *testing.T) {
	dsn := requireSyncE2EDSN(t)
	pool := openHub(t)
	ctx := context.Background()

	// No SetDefaultAuthor and no MTIX_AUTHOR_ID → both emit as 'cli'.
	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")

	a.createNode(t, "PRJ-1", "initial")
	a.pushAll(ctx, t, pool)
	require.Equal(t, 1, b.pullAll(ctx, t, pool))

	a.updateTitle(t, "PRJ-1", "from-A")
	b.updateTitle(t, "PRJ-1", "from-B")
	a.pushAll(ctx, t, pool)
	_, bConflicts := b.pushAll(ctx, t, pool)

	assert.Zero(t, bConflicts,
		"a shared 'cli' author → VC-Equal edits → the hub records nothing (the pre-MTIX-24 gap)")
	assert.Zero(t, hubConflictCount(t, dsn))
}
