// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// A legitimate reader-pool barrier delays any postcommit read until a later
// title edit commits. A method that retains its transaction pair still passes,
// even if it performs an incidental later read. No production hook is used.
func TestAtomicUpdatePrompt_ReadDelayedAfterCommit_ReturnsCommitHashes(t *testing.T) {
	s, before := newPromptAtomicFixture(t)
	api := requirePromptAtomicAPI(t, s)
	ctx := promptAtomicContext(t)
	s.writeDB.SetMaxOpenConns(1)
	tx, err := s.writeDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	staged := stagePromptCompetingEdit(ctx, t, tx, promptCompetingEdit{s, before, "title"})
	wantAfter := staged
	wantAfter.Prompt = "replacementprompttoken"
	waits := s.writeDB.Stats().WaitCount
	done := make(chan promptAtomicResult, 1)
	go func() {
		oldHash, newHash, updateErr := api.UpdatePrompt(ctx, before.ID, wantAfter.Prompt)
		done <- promptAtomicResult{oldHash, newHash, updateErr}
	}()
	waitForPromptWriter(ctx, t, s, done, waits)
	s.readDB.SetMaxOpenConns(1)
	reader, err := s.readDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	waits = s.readDB.Stats().WaitCount
	require.NoError(t, tx.Commit())
	result, returned := waitForPromptResultOrReader(ctx, t, s, done, waits)
	if !returned {
		editAfterPromptCommit(ctx, t, s, wantAfter)
	}
	require.NoError(t, reader.Close())
	if !returned {
		result = awaitPromptAtomicResult(ctx, t, done)
	}
	require.NoError(t, result.err)
	assert.Equal(t, staged.ComputeHash(), result.oldHash)
	assert.Equal(t, wantAfter.ComputeHash(), result.newHash)
	after, err := s.GetNode(ctx, before.ID)
	require.NoError(t, err)
	assert.Equal(t, after.ComputeHash(), after.ContentHash)
	assert.Equal(t, wantAfter.Prompt, after.Prompt)
	if !returned {
		assert.NotEqual(t, after.ContentHash, result.newHash)
	}
}

func waitForPromptWriter(ctx context.Context, t *testing.T, s *Store, done <-chan promptAtomicResult, waits int64) {
	t.Helper()
	for s.writeDB.Stats().WaitCount <= waits {
		select {
		case result := <-done:
			t.Fatalf("returned before writer release: %+v", result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			runtime.Gosched()
		}
	}
}

func waitForPromptResultOrReader(ctx context.Context, t *testing.T, s *Store, done <-chan promptAtomicResult, waits int64) (promptAtomicResult, bool) {
	t.Helper()
	for {
		select {
		case result := <-done:
			return result, true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
			if s.readDB.Stats().WaitCount > waits {
				return promptAtomicResult{}, false
			}
			runtime.Gosched()
		}
	}
}

func editAfterPromptCommit(ctx context.Context, t *testing.T, s *Store, want model.Node) {
	t.Helper()
	var prompt, hash string
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `SELECT prompt, content_hash FROM nodes WHERE id=?`, want.ID).Scan(&prompt, &hash))
	require.Equal(t, want.Prompt, prompt, "prompt must really commit before the later edit")
	require.Equal(t, want.ComputeHash(), hash)
	title := "Later committed title"
	require.NoError(t, s.UpdateNode(ctx, want.ID, &store.NodeUpdate{Title: &title}))
}

func awaitPromptAtomicResult(ctx context.Context, t *testing.T, done <-chan promptAtomicResult) promptAtomicResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return promptAtomicResult{}
	}
}
