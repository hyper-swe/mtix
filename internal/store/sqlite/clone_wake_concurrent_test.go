// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func cloneBootstrapOtherHandle(t *testing.T, s *Store) *Store {
	t.Helper()
	other, err := New(s.dbPath, s.logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	return other
}

func cloneBootstrapLocalMail(t *testing.T) *model.SyncEvent {
	t.Helper()
	e := makeApplyEvent(t, model.OpComment, "TEST-1", "local-worker", 2,
		&model.CommentPayload{AuthorID: "local-worker", Body: "fresh addressed mail", To: "worker"})
	e.ProjectPrefix = "TEST"
	return e
}

func assertCloneBootstrapFloorsBelowMail(t *testing.T, raw *sql.DB, e *model.SyncEvent) {
	t.Helper()
	var seq int64
	require.NoError(t, raw.QueryRow(`SELECT rowid FROM sync_events WHERE event_id=?`, e.EventID).Scan(&seq))
	for _, mark := range cloneBootstrapMarks(t, raw) {
		assert.Less(t, mark, seq, "no floor may cover addressed local mail")
	}
}

func TestCloneWakeBootstrap_ConcurrentWriterBeforeBatch_PreservesLocalMail(t *testing.T) {
	s, raw := cloneBootstrapFixture(t, "claude")
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-1", 1)))
	other := cloneBootstrapOtherHandle(t, s)
	local := cloneBootstrapLocalMail(t)
	next := cloneBootstrapEvent(t, "TEST-2", 3)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	held, err := s.WriteDB().Conn(ctx)
	require.NoError(t, err)
	release := sync.OnceFunc(func() { assert.NoError(t, held.Close()) })
	defer release()
	waits := s.WriteDB().Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		done <- s.WithCloneWakeBootstrap(ctx, func(tx *sql.Tx) error { return s.IdempotentApply(ctx, tx, next) })
	}()
	require.Eventually(t, func() bool { return s.WriteDB().Stats().WaitCount > waits }, 5*time.Second, time.Millisecond)
	require.NoError(t, applyOnce(t, other, local))
	release()
	require.NoError(t, <-done)
	assert.Equal(t, []int64{1, 1, 1, 1}, cloneBootstrapMarks(t, raw))
	assert.Empty(t, cloneBootstrapWitness(t, raw))
	assertCloneBootstrapFloorsBelowMail(t, raw, local)
}

// A post-commit writer catches a snapshot read in a separately committed writer
// transaction: that extra commit would expose a real local-write gap before apply.
func TestCloneWakeBootstrap_NoCommitBetweenWitnessAndBatch(t *testing.T) {
	s, raw := cloneBootstrapFixture(t, "pi")
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-1", 1)))
	other := cloneBootstrapOtherHandle(t, s)
	local := cloneBootstrapLocalMail(t)
	writes := 0
	var writeErr error
	s.AddOnCommit(func() {
		if writes == 0 {
			writes++
			writeErr = other.WithTx(t.Context(), func(tx *sql.Tx) error { return other.IdempotentApply(t.Context(), tx, local) })
		}
	})
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-2", 3)))
	require.Equal(t, 1, writes)
	require.NoError(t, writeErr)
	assertCloneBootstrapFloorsBelowMail(t, raw, local)
}
