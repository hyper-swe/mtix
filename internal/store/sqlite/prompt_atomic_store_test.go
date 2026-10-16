// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// promptAtomicStoreAPI is asserted against the real SQLite Store. Absence on
// the old base is an API-absence failure, not a functional concurrency RED.
type promptAtomicStoreAPI interface {
	UpdatePrompt(context.Context, string, string) (string, string, error)
}

func requirePromptAtomicAPI(t *testing.T, s *Store) promptAtomicStoreAPI {
	t.Helper()
	api, ok := any(s).(promptAtomicStoreAPI)
	require.True(t, ok, "API absence: real SQLite Store must implement atomic UpdatePrompt")
	return api
}

func newPromptAtomicFixture(t *testing.T) (*Store, *model.Node) {
	t.Helper()
	s := newInternalTestStore(t)
	n := makeTestNode("PROJ-1", "", "PROJ", "Before title", 0, 1, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	n.Description, n.Prompt = "Before description", "obsoleteprompttoken"
	n.ContentHash = n.ComputeHash()
	require.NoError(t, s.CreateNode(context.Background(), n))
	got, err := s.GetNode(context.Background(), n.ID)
	require.NoError(t, err)
	return s, got
}

func promptAtomicContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type promptAtomicResult struct {
	oldHash, newHash string
	err              error
}

type promptCompetingEdit struct {
	store *Store
	node  *model.Node
	field string
}

// stagePromptCompetingEdit holds the original assertions in a bounded helper.
func stagePromptCompetingEdit(ctx context.Context, t *testing.T, tx *sql.Tx, edit promptCompetingEdit) model.Node {
	t.Helper()
	s, original, field := edit.store, edit.node, edit.field
	value := "Competing edit"
	update := &store.NodeUpdate{}
	if field == "title" {
		update.Title = &value
	} else {
		update.Description = &value
	}
	require.NoError(t, s.inTxHistory(ctx, tx, historyScope{id: original.ID}, func() error {
		return s.executeUpdateTx(ctx, tx, original.ID, update)
	}))
	beforePrompt := *original
	if field == "title" {
		beforePrompt.Title = value
	} else {
		beforePrompt.Description = value
	}
	expectedOld := beforePrompt.ComputeHash()
	var stagedHash string
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT content_hash FROM nodes WHERE id=?`, original.ID).Scan(&stagedHash))
	require.Equal(t, expectedOld, stagedHash)
	// Confirm the separate read pool sees the old committed row while held.
	visible, err := s.GetNode(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, original.ContentHash, visible.ContentHash)
	return beforePrompt
}

// This barrier lives inside the real store's writer acquisition gap, not in a
// service wrapper. A held BEGIN IMMEDIATE transaction stages the competing edit
// while WAL readers still see the previous row. A bad atomic method that calls
// GetNode before opening its write transaction reads that previous snapshot,
// then queues for the sole writer connection. A correct method queues first.
func TestAtomicUpdatePrompt_WriterQueue_HashesFromTransactionRow(t *testing.T) {
	for _, field := range []string{"title", "description"} {
		t.Run(field, func(t *testing.T) {
			s, original := newPromptAtomicFixture(t)
			api := requirePromptAtomicAPI(t, s)
			s.writeDB.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := s.writeDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			beforePrompt := stagePromptCompetingEdit(ctx, t, tx, promptCompetingEdit{s, original, field})
			expectedOld := beforePrompt.ComputeHash()
			waitsBefore := s.writeDB.Stats().WaitCount
			done := make(chan promptAtomicResult, 1)
			go func() {
				oldHash, newHash, err := api.UpdatePrompt(ctx, original.ID, "replacementprompttoken")
				done <- promptAtomicResult{oldHash, newHash, err}
			}()
			// WaitCount is monotonic and only this goroutine requests the held pool.
			// No sleep approximates when its writer acquisition has actually begun.
			for s.writeDB.Stats().WaitCount <= waitsBefore {
				select {
				case result := <-done:
					t.Fatalf("atomic method returned before writer release: %+v", result)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
					runtime.Gosched()
				}
			}
			require.NoError(t, tx.Commit())
			var result promptAtomicResult
			select {
			case result = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.NoError(t, result.err)
			afterPrompt, err := s.GetNode(ctx, original.ID)
			require.NoError(t, err)
			expectedAfter := beforePrompt
			expectedAfter.Prompt = "replacementprompttoken"
			assert.Equal(t, expectedOld, result.oldHash)
			assert.Equal(t, expectedAfter.ComputeHash(), result.newHash)
			assert.Equal(t, result.newHash, afterPrompt.ContentHash)
			assert.Equal(t, afterPrompt.ComputeHash(), afterPrompt.ContentHash)
			assert.Equal(t, expectedAfter.Title, afterPrompt.Title)
			assert.Equal(t, expectedAfter.Description, afterPrompt.Description)
			assert.Equal(t, expectedAfter.Prompt, afterPrompt.Prompt)
		})
	}
}

func promptAtomicCount(t *testing.T, s *Store, query string) int {
	t.Helper()
	var n int
	require.NoError(t, s.readDB.QueryRowContext(context.Background(), query).Scan(&n))
	return n
}

func assertPromptAtomicUnchanged(t *testing.T, s *Store, before *model.Node, events, history int) {
	t.Helper()
	got, err := s.GetNode(context.Background(), before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.Prompt, got.Prompt)
	assert.Equal(t, before.ContentHash, got.ContentHash)
	assert.Equal(t, events, promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`))
	assert.Equal(t, history, promptAtomicCount(t, s, `SELECT count(*) FROM task_history`))
}

func TestAtomicUpdatePrompt_HistorySyncAndFTSPreserved(t *testing.T) {
	s, before := newPromptAtomicFixture(t)
	api := requirePromptAtomicAPI(t, s)
	events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`)
	history := promptAtomicCount(t, s, `SELECT count(*) FROM task_history`)
	prompts := promptAtomicCount(t, s, `SELECT count(*) FROM task_history WHERE field='prompt'`)
	setPrompts := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events WHERE op_type='set_prompt'`)
	oldHash, newHash, err := api.UpdatePrompt(promptAtomicContext(t), before.ID, "replacementprompttoken")
	require.NoError(t, err)
	after, err := s.GetNode(context.Background(), before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.ContentHash, oldHash)
	assert.Equal(t, after.ComputeHash(), newHash)
	assert.Equal(t, newHash, after.ContentHash)
	assert.Equal(t, "replacementprompttoken", after.Prompt)
	assert.Equal(t, events+1, promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`))
	assert.Equal(t, setPrompts+1, promptAtomicCount(t, s, `SELECT count(*) FROM sync_events WHERE op_type='set_prompt'`))
	assert.Equal(t, history+1, promptAtomicCount(t, s, `SELECT count(*) FROM task_history`))
	assert.Equal(t, prompts+1, promptAtomicCount(t, s, `SELECT count(*) FROM task_history WHERE field='prompt'`))
	assert.Zero(t, promptAtomicCount(t, s, `SELECT count(*) FROM task_history WHERE field='prompt' AND (old_value IS NOT NULL OR new_value IS NOT NULL)`))
	var payload string
	require.NoError(t, s.readDB.QueryRow(`SELECT payload FROM sync_events WHERE op_type='set_prompt' ORDER BY lamport_clock DESC LIMIT 1`).Scan(&payload))
	var decoded model.SetPromptPayload
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded))
	assert.Equal(t, "replacementprompttoken", decoded.PromptText)
	assert.Equal(t, 1, promptAtomicCount(t, s, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'replacementprompttoken'`))
	assert.Zero(t, promptAtomicCount(t, s, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'obsoleteprompttoken'`))
}

func TestAtomicUpdatePrompt_MissingCanceled_EmptyHashes(t *testing.T) {
	for _, reason := range []string{"missing", "canceled"} {
		t.Run(reason, func(t *testing.T) {
			s, before := newPromptAtomicFixture(t)
			api := requirePromptAtomicAPI(t, s)
			events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`)
			history := promptAtomicCount(t, s, `SELECT count(*) FROM task_history`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id, expected := before.ID, model.ErrNotFound
			if reason == "missing" {
				id = "PROJ-999999"
			} else {
				cancel()
				expected = context.Canceled
			}
			oldHash, newHash, err := api.UpdatePrompt(ctx, id, "uncommittedprompttoken")
			require.ErrorIs(t, err, expected)
			assert.Empty(t, oldHash)
			assert.Empty(t, newHash)
			assertPromptAtomicUnchanged(t, s, before, events, history)
		})
	}
}

func TestAtomicUpdatePrompt_SyncHistoryFailure_RollbackAndEmptyHashes(t *testing.T) {
	cases := []struct{ name, trigger, message string }{
		{"sync", `CREATE TRIGGER prompt_atomic_block_sync BEFORE INSERT ON sync_events WHEN NEW.op_type='set_prompt' BEGIN SELECT RAISE(ABORT,'prompt sync rejected'); END`, "prompt sync rejected"},
		{"history", `CREATE TRIGGER prompt_atomic_block_history BEFORE INSERT ON task_history WHEN NEW.field='prompt' BEGIN SELECT RAISE(ABORT,'prompt history rejected'); END`, "prompt history rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, before := newPromptAtomicFixture(t)
			api := requirePromptAtomicAPI(t, s)
			events := promptAtomicCount(t, s, `SELECT count(*) FROM sync_events`)
			history := promptAtomicCount(t, s, `SELECT count(*) FROM task_history`)
			_, err := s.writeDB.Exec(tc.trigger)
			require.NoError(t, err)
			oldHash, newHash, err := api.UpdatePrompt(promptAtomicContext(t), before.ID, "uncommittedprompttoken")
			require.ErrorContains(t, err, tc.message)
			assert.Empty(t, oldHash)
			assert.Empty(t, newHash)
			assertPromptAtomicUnchanged(t, s, before, events, history)
			assert.Zero(t, promptAtomicCount(t, s, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'uncommittedprompttoken'`))
			assert.Equal(t, 1, promptAtomicCount(t, s, `SELECT count(*) FROM nodes_fts WHERE nodes_fts MATCH 'obsoleteprompttoken'`))
		})
	}
}

// A deferred FK permits every callback statement, then rejects COMMIT. The
// test only adds an isolated fixture table/trigger. v1.35.0 may leave its writer
// connection in that failed transaction; the explicit cleanup below prevents
// this control from silently claiming that WithTx already cleans such errors.
func TestAtomicUpdatePrompt_CommitFailure_EmptyHashes(t *testing.T) {
	s, before := newPromptAtomicFixture(t)
	api := requirePromptAtomicAPI(t, s)
	for _, statement := range []string{
		`CREATE TABLE prompt_atomic_parent(id INTEGER PRIMARY KEY)`,
		`CREATE TABLE prompt_atomic_deferred(parent_id INTEGER REFERENCES prompt_atomic_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TRIGGER prompt_atomic_commit_failure AFTER UPDATE OF prompt ON nodes BEGIN INSERT INTO prompt_atomic_deferred(parent_id) VALUES(404); END`,
	} {
		_, err := s.writeDB.Exec(statement)
		require.NoError(t, err)
	}
	// database/sql marks a failed Commit Tx done, so Tx.Rollback cannot clean
	// the driver connection. Raw ROLLBACK is fixture cleanup, not production fix.
	t.Cleanup(func() { _, _ = s.writeDB.Exec(`ROLLBACK`) })
	oldHash, newHash, err := api.UpdatePrompt(promptAtomicContext(t), before.ID, "uncommittedprompttoken")
	require.ErrorContains(t, err, "FOREIGN KEY constraint failed")
	assert.Empty(t, oldHash)
	assert.Empty(t, newHash)
	// Do not assert rollback or writer reuse here: those are separate preexisting
	// driver/WithTx concerns. Empty API results are the scope of this control.
}
