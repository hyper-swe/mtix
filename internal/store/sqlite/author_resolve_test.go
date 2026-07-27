// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// MTIX-24: resolveEmitAuthor picks the emit author by precedence — explicit
// (e.g. claim's agentID) > MTIX_AUTHOR_ID env > meta.sync.author_id > 'cli' —
// so same-machine agents get distinct authors and the hub logs their conflicts.

func TestResolveEmitAuthor_Precedence(t *testing.T) {
	_, raw := applyTestStore(t)
	ctx := context.Background()
	begin := func() *sql.Tx {
		tx, err := raw.BeginTx(ctx, nil)
		require.NoError(t, err)
		return tx
	}

	t.Setenv(AuthorIDEnv, "env-agent")
	_, err := raw.ExecContext(ctx, `UPDATE meta SET value = 'meta-agent' WHERE key = 'meta.sync.author_id'`)
	require.NoError(t, err)

	// 1. An explicit author beats env and meta.
	tx := begin()
	assert.Equal(t, "claim-agent", resolveEmitAuthor(ctx, tx, "claim-agent"))
	_ = tx.Rollback()

	// 2. With no explicit author, the env override wins over meta.
	tx = begin()
	assert.Equal(t, "env-agent", resolveEmitAuthor(ctx, tx, ""))
	_ = tx.Rollback()

	// 3. With env cleared, the persisted meta default is used.
	t.Setenv(AuthorIDEnv, "")
	tx = begin()
	assert.Equal(t, "meta-agent", resolveEmitAuthor(ctx, tx, ""))
	_ = tx.Rollback()

	// 4. With env and meta both empty, fall back to 'cli'.
	_, err = raw.ExecContext(ctx, `UPDATE meta SET value = '' WHERE key = 'meta.sync.author_id'`)
	require.NoError(t, err)
	tx = begin()
	assert.Equal(t, authorIDFallback, resolveEmitAuthor(ctx, tx, ""))
	_ = tx.Rollback()
}

// TestEmitUsesEnvAuthor proves the resolution reaches the real emit path: with
// MTIX_AUTHOR_ID set, a content update stamps the emitted sync_events row with
// that author (previously always 'cli').
func TestEmitUsesEnvAuthor(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()

	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "MTIX-1", Project: "MTIX", Depth: 0, Seq: 1, Title: "t",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	t.Setenv(AuthorIDEnv, "agent-x")
	title := "edited"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Title: &title}))

	var author string
	require.NoError(t, raw.QueryRowContext(ctx,
		`SELECT author_id FROM sync_events WHERE node_id = 'MTIX-1' AND op_type = 'update_field'
		 ORDER BY rowid DESC LIMIT 1`).Scan(&author))
	assert.Equal(t, "agent-x", author,
		"an update emitted with MTIX_AUTHOR_ID set must carry that author, not 'cli'")
}
