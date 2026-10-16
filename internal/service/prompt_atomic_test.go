// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// atomicPromptWriter lets the test compile before Store gains the atomic API.
// It is only invoked when PromptService actually calls that API; there is no
// simulated implementation or read-update-read fallback in this adapter.
type atomicPromptWriter interface {
	UpdatePrompt(context.Context, string, string) (string, string, error)
}

// promptWriteBarrier supports both the legacy snapshot path and the atomic API.
// All barriers are outside SQLite transactions, so the competing writer can
// commit without contention. once also lets an incorrect postcommit GetNode
// proceed, where it will observe the later edit and fail the metadata assertion.
type promptWriteBarrier struct {
	store.Store
	beforeOnce   sync.Once
	before       chan struct{}
	resumeBefore chan struct{}
	committed    chan struct{}
	resumeAfter  chan struct{}
	fail         error
}

func (s *promptWriteBarrier) pauseBefore(ctx context.Context) error {
	if s.before == nil {
		return nil
	}
	var err error
	s.beforeOnce.Do(func() {
		close(s.before)
		select {
		case <-s.resumeBefore:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return err
}

func (s *promptWriteBarrier) pauseAfter(ctx context.Context) error {
	if s.committed == nil {
		return nil
	}
	close(s.committed)
	select {
	case <-s.resumeAfter:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *promptWriteBarrier) GetNode(ctx context.Context, id string) (*model.Node, error) {
	n, err := s.Store.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.pauseBefore(ctx); err != nil {
		return nil, err
	}
	return n, nil
}

func (s *promptWriteBarrier) UpdateNode(ctx context.Context, id string, update *store.NodeUpdate) error {
	if s.fail != nil {
		return s.fail
	}
	if err := s.Store.UpdateNode(ctx, id, update); err != nil {
		return err
	}
	return s.pauseAfter(ctx)
}

func (s *promptWriteBarrier) UpdatePrompt(ctx context.Context, id, text string) (string, string, error) {
	if err := s.pauseBefore(ctx); err != nil {
		return "", "", err
	}
	if s.fail != nil {
		return "", "", s.fail
	}
	writer, ok := s.Store.(atomicPromptWriter)
	if !ok {
		return "", "", errors.New("real store does not implement atomic UpdatePrompt")
	}
	oldHash, newHash, err := writer.UpdatePrompt(ctx, id, text)
	if err != nil {
		return "", "", err
	}
	if err := s.pauseAfter(ctx); err != nil {
		return "", "", err
	}
	return oldHash, newHash, nil
}

func waitPromptBarrier(ctx context.Context, t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func waitPromptResult(ctx context.Context, t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return ctx.Err()
	}
}

func promptMetadata(t *testing.T, bc *recordingBroadcaster, id string) map[string]string {
	t.Helper()
	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventNodeUpdated, events[0].Type)
	assert.Equal(t, id, events[0].NodeID)
	assert.Equal(t, "editor", events[0].Author)
	metadata := map[string]string{}
	require.NoError(t, json.Unmarshal(events[0].Data, &metadata))
	return metadata
}

// On the legacy implementation the snapshot precedes the competing edit.
// On the atomic implementation its write begins after that edit commits.
// Both cases have the same expected immediate pre/post-prompt row hashes.
func TestUpdatePrompt_ConcurrentContentEdit_HashesDescribeCommittedRows(t *testing.T) {
	for _, field := range []string{"title", "description"} {
		t.Run(field, func(t *testing.T) {
			_, nodeSvc, s, bc := newTestPromptService(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			n, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
				Project: "PROJ", Title: "Before title", Description: "Before description", Prompt: "Before prompt", Creator: "admin",
			})
			require.NoError(t, err)
			bc.Reset()
			barrier := &promptWriteBarrier{Store: s, before: make(chan struct{}), resumeBefore: make(chan struct{})}
			svc := service.NewPromptService(barrier, bc, slog.Default(), fixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)))
			done := make(chan error, 1)
			go func() { done <- svc.UpdatePrompt(ctx, n.ID, "After prompt", "editor") }()
			waitPromptBarrier(ctx, t, barrier.before)
			value := "Competing edit"
			update := &store.NodeUpdate{}
			if field == "title" {
				update.Title = &value
			} else {
				update.Description = &value
			}
			require.NoError(t, s.UpdateNode(ctx, n.ID, update))
			beforePrompt, err := s.GetNode(ctx, n.ID)
			require.NoError(t, err)
			close(barrier.resumeBefore)
			require.NoError(t, waitPromptResult(ctx, t, done))
			afterPrompt, err := s.GetNode(ctx, n.ID)
			require.NoError(t, err)
			t.Run("stored_hash_preservation", func(t *testing.T) {
				assert.Equal(t, "After prompt", afterPrompt.Prompt)
				if field == "title" {
					assert.Equal(t, value, afterPrompt.Title)
				} else {
					assert.Equal(t, value, afterPrompt.Description)
				}
				assert.Equal(t, afterPrompt.ComputeHash(), afterPrompt.ContentHash)
			})
			metadata := promptMetadata(t, bc, n.ID)
			t.Run("old_hash_at_prompt_commit", func(t *testing.T) { assert.Equal(t, beforePrompt.ContentHash, metadata["old_prompt_hash"]) })
			t.Run("new_hash_at_prompt_commit", func(t *testing.T) { assert.Equal(t, afterPrompt.ContentHash, metadata["new_prompt_hash"]) })
		})
	}
}

// The second writer commits after the prompt store method commits, but before
// it returns to the service. A service read after that return sees the later
// title hash; the event must instead retain hashes from the prompt transaction.
// This is already green on the legacy service, whose snapshot is current here.
func TestUpdatePrompt_EditAfterCommit_HashesDescribePromptCommit(t *testing.T) {
	_, nodeSvc, s, bc := newTestPromptService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Before title", Prompt: "Before prompt", Creator: "admin"})
	require.NoError(t, err)
	beforePrompt, err := s.GetNode(ctx, n.ID)
	require.NoError(t, err)
	bc.Reset()
	barrier := &promptWriteBarrier{Store: s, committed: make(chan struct{}), resumeAfter: make(chan struct{})}
	svc := service.NewPromptService(barrier, bc, slog.Default(), fixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)))
	done := make(chan error, 1)
	go func() { done <- svc.UpdatePrompt(ctx, n.ID, "After prompt", "editor") }()
	waitPromptBarrier(ctx, t, barrier.committed)
	atPromptCommit, err := s.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, "After prompt", atPromptCommit.Prompt)
	title := "Later edit"
	require.NoError(t, s.UpdateNode(ctx, n.ID, &store.NodeUpdate{Title: &title}))
	later, err := s.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.NotEqual(t, atPromptCommit.ContentHash, later.ContentHash)
	close(barrier.resumeAfter)
	require.NoError(t, waitPromptResult(ctx, t, done))
	assert.Equal(t, later.ComputeHash(), later.ContentHash)
	metadata := promptMetadata(t, bc, n.ID)
	assert.Equal(t, beforePrompt.ContentHash, metadata["old_prompt_hash"])
	assert.Equal(t, atPromptCommit.ContentHash, metadata["new_prompt_hash"])
	assert.NotEqual(t, later.ContentHash, metadata["new_prompt_hash"])
}

func TestUpdatePrompt_FailedWrite_DoesNotBroadcast(t *testing.T) {
	_, nodeSvc, s, bc := newTestPromptService(t)
	ctx := context.Background()
	n, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Failure control", Prompt: "Original", Creator: "admin"})
	require.NoError(t, err)
	sentinel := errors.New("injected prompt write failure")
	barrier := &promptWriteBarrier{Store: s, fail: sentinel}
	svc := service.NewPromptService(barrier, bc, slog.Default(), fixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)))
	bc.Reset()
	require.ErrorIs(t, svc.UpdatePrompt(ctx, n.ID, "Uncommitted", "editor"), sentinel)
	require.Empty(t, bc.Events())
	got, err := s.GetNode(ctx, n.ID)
	require.NoError(t, err)
	assert.Equal(t, "Original", got.Prompt)
	assert.Equal(t, n.ContentHash, got.ContentHash)
}

func TestUpdatePrompt_MissingOrCanceled_DoesNotBroadcast(t *testing.T) {
	for _, reason := range []string{"missing", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			svc, nodeSvc, s, bc := newTestPromptService(t)
			n, err := nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "PROJ", Title: "No event control", Prompt: "Original", Creator: "admin"})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			id := n.ID
			expected := model.ErrNotFound
			if reason == "missing" {
				id = "PROJ-999999"
			} else {
				cancel()
				expected = context.Canceled
			}
			bc.Reset()
			require.ErrorIs(t, svc.UpdatePrompt(ctx, id, "Uncommitted", "editor"), expected)
			require.Empty(t, bc.Events())
			got, err := s.GetNode(context.Background(), n.ID)
			require.NoError(t, err)
			assert.Equal(t, "Original", got.Prompt)
			assert.Equal(t, n.ContentHash, got.ContentHash)
		})
	}
}
