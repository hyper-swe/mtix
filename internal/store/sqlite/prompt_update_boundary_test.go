// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAtomicUpdatePrompt_RepeatedPrompt_PreservesSyncEvents(t *testing.T) {
	for _, text := range []string{"", "obsoleteprompttoken"} {
		t.Run("prompt="+text, func(t *testing.T) {
			s, before := newPromptAtomicFixture(t)
			api := requirePromptAtomicAPI(t, s)
			ctx := promptAtomicContext(t)
			events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events WHERE op_type='set_prompt'`)
			for request := 0; request < 2; request++ {
				oldHash, newHash, err := api.UpdatePrompt(ctx, before.ID, text)
				require.NoError(t, err)
				after, err := s.GetNode(ctx, before.ID)
				require.NoError(t, err)
				assert.Equal(t, text, after.Prompt)
				assert.Equal(t, before.ContentHash, oldHash)
				assert.Equal(t, after.ComputeHash(), newHash)
				assert.Equal(t, newHash, after.ContentHash)
				assert.Equal(t, events+request+1, promptAtomicCount(t, s, `SELECT count(*) FROM sync_events WHERE op_type='set_prompt'`))
				before = after
			}
		})
	}
}

func TestAtomicUpdatePrompt_SoftDeleted_EmptyHashes(t *testing.T) {
	s, before := newPromptAtomicFixture(t)
	api := requirePromptAtomicAPI(t, s)
	ctx := promptAtomicContext(t)
	require.NoError(t, s.DeleteNode(ctx, before.ID, false, "editor"))
	events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`)
	history := promptAtomicCount(t, s, `SELECT count(*) FROM task_history`)
	oldHash, newHash, err := api.UpdatePrompt(ctx, before.ID, "uncommittedprompttoken")
	require.ErrorIs(t, err, model.ErrNotFound)
	assert.Empty(t, oldHash)
	assert.Empty(t, newHash)
	var prompt, hash string
	require.NoError(t, s.readDB.QueryRowContext(ctx, `SELECT prompt, content_hash FROM nodes WHERE id=?`, before.ID).Scan(&prompt, &hash))
	assert.Equal(t, before.Prompt, prompt)
	assert.Equal(t, before.ContentHash, hash)
	assert.Equal(t, events, promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`))
	assert.Equal(t, history, promptAtomicCount(t, s, `SELECT count(*) FROM task_history`))
}

func TestAtomicUpdatePrompt_HashReadFailure_EmptyHashes(t *testing.T) {
	s, before := newPromptAtomicFixture(t)
	api := requirePromptAtomicAPI(t, s)
	ctx := promptAtomicContext(t)
	events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`)
	history := promptAtomicCount(t, s, `SELECT count(*) FROM task_history`)
	// Fixture-only DDL makes the transaction's real hash query fail. This does
	// not install a production hook or claim that API absence is functional RED.
	_, err := s.writeDB.ExecContext(ctx, `ALTER TABLE nodes RENAME COLUMN content_hash TO unavailable_hash`)
	require.NoError(t, err)
	oldHash, newHash, err := api.UpdatePrompt(ctx, before.ID, "uncommittedprompttoken")
	require.ErrorContains(t, err, "content_hash")
	assert.Empty(t, oldHash)
	assert.Empty(t, newHash)
	var prompt, hash string
	require.NoError(t, s.readDB.QueryRowContext(ctx, `SELECT prompt, unavailable_hash FROM nodes WHERE id=?`, before.ID).Scan(&prompt, &hash))
	assert.Equal(t, before.Prompt, prompt)
	assert.Equal(t, before.ContentHash, hash)
	assert.Equal(t, events, promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`))
	assert.Equal(t, history, promptAtomicCount(t, s, `SELECT count(*) FROM task_history`))
}

func TestAtomicUpdatePrompt_HashUnavailableAfterSync_RollbackAndEmptyHashes(t *testing.T) {
	s, before := newPromptAtomicFixture(t)
	api := requirePromptAtomicAPI(t, s)
	ctx := promptAtomicContext(t)
	events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`)
	history := promptAtomicCount(t, s, `SELECT count(*) FROM task_history`)
	// A fixture-only trigger makes the edited row unavailable after its sync
	// event is inserted, exercising a failed final read before commit.
	_, err := s.writeDB.ExecContext(ctx, `CREATE TRIGGER prompt_atomic_hide_after_sync
		AFTER INSERT ON sync_events WHEN NEW.op_type='set_prompt'
		BEGIN UPDATE nodes SET deleted_at='2026-03-10T12:00:00Z' WHERE id=NEW.node_id; END`)
	require.NoError(t, err)
	oldHash, newHash, err := api.UpdatePrompt(ctx, before.ID, "uncommittedprompttoken")
	require.ErrorIs(t, err, model.ErrNotFound)
	assert.Empty(t, oldHash)
	assert.Empty(t, newHash)
	assertPromptAtomicUnchanged(t, s, before, events, history)
	assert.Zero(t, promptAtomicCount(t, s, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'uncommittedprompttoken'`))
	assert.Equal(t, 1, promptAtomicCount(t, s, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'obsoleteprompttoken'`))
}
