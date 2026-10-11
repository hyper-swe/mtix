// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Parallel cases retain owned fixtures and existing behavior assertions.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/stretchr/testify/require"
)

// observeAuthorsViaApply applies one foreign event per author, each from
// a distinct author id, so the stored vector clock grows by merge.
func observeAuthorsViaApply(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		author := fmt.Sprintf("agent-%03d", i)
		e := makeApplyEvent(t, model.OpCreateNode, fmt.Sprintf("MTIX-%d", i+1), author,
			int64(i+1), map[string]any{"title": "t"})
		require.NoError(t, applyOnce(t, s, e))
	}
}

// emitOne emits one event as author and returns the stored event.
func emitOne(t *testing.T, s *Store, raw *sql.DB, author string) *model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID: "MTIX-1", ProjectCode: "MTIX", OpType: model.OpCreateNode,
			Author: author, Payload: json.RawMessage(`{"title":"x"}`),
		})
	}))
	return readOneEvent(t, raw)
}

func storedVC(t *testing.T, raw *sql.DB) model.VectorClock {
	t.Helper()
	var v string
	require.NoError(t, raw.QueryRow(`SELECT value FROM meta WHERE key='meta.sync.vector_clock'`).Scan(&v))
	vc := model.VectorClock{}
	require.NoError(t, json.Unmarshal([]byte(v), &vc))
	return vc
}

// TestEmit_ManyAuthors_NeverFails: a local write never fails because of
// the vector clock's size, however the authors were observed (MTIX-95.16).
func TestEmit_ManyAuthors_NeverFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		authors int
		observe func(t *testing.T, s *Store, raw *sql.DB, n int)
	}{
		{"150 authors via apply", 150, func(t *testing.T, s *Store, _ *sql.DB, n int) { observeAuthorsViaApply(t, s, n) }},
		{"100 authors via apply (boundary)", 100, func(t *testing.T, s *Store, _ *sql.DB, n int) { observeAuthorsViaApply(t, s, n) }},
		{"150 authors via emit", 150, func(t *testing.T, s *Store, raw *sql.DB, n int) {
			for i := 0; i < n; i++ {
				emitOne(t, s, raw, fmt.Sprintf("local-%03d", i))
			}
		}},
		{"300 authors via apply", 300, func(t *testing.T, s *Store, _ *sql.DB, n int) { observeAuthorsViaApply(t, s, n) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, raw := emitTestStore(t)
			tt.observe(t, s, raw, tt.authors)

			ev := emitOne(t, s, raw, "me")
			require.NoError(t, ev.VectorClock.Validate())
			require.LessOrEqual(t, len(ev.VectorClock), model.MaxVectorClockEntries)
			require.Positive(t, ev.VectorClock["me"], "local author kept")
			require.NoError(t, validator.ValidateBatch([]*model.SyncEvent{ev}, time.Now(), &validator.Result{}))
			require.LessOrEqual(t, len(storedVC(t, raw)), model.MaxVectorClockEntries)
		})
	}
}

// TestApply_ManyAuthors_StoredClockStaysBounded: merging never leaves the
// stored clock over the cap, and the local author's counter survives.
func TestApply_ManyAuthors_StoredClockStaysBounded(t *testing.T) {
	t.Setenv(AuthorIDEnv, "") // default local author resolves to the fallback
	s, raw := emitTestStore(t)
	for i := 0; i < 5; i++ {
		emitOne(t, s, raw, "")
	}
	observeAuthorsViaApply(t, s, 250)
	vc := storedVC(t, raw)
	require.LessOrEqual(t, len(vc), model.MaxVectorClockEntries)
	require.Equal(t, int64(5), vc[authorIDFallback], "default local author counter never dropped or reset")
	ev := emitOne(t, s, raw, "")
	require.Equal(t, int64(6), ev.VectorClock[authorIDFallback])
}

func TestEmit_ManyAuthors_ConcurrentWritersNeverFail(t *testing.T) {
	t.Parallel()
	s, raw := emitTestStore(t)
	observeAuthorsViaApply(t, s, 120)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			ctx := context.Background()
			errs <- s.WithTx(ctx, func(tx *sql.Tx) error {
				return emitEvent(ctx, tx, emitParams{
					NodeID: "MTIX-1", ProjectCode: "MTIX", OpType: model.OpCreateNode,
					Author: fmt.Sprintf("w%d", w), Payload: json.RawMessage(`{"title":"x"}`),
				})
			})
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.LessOrEqual(t, len(storedVC(t, raw)), model.MaxVectorClockEntries)
}

// TestEmit_ExplicitAuthorCounterSurvivesPrune: an author that emitted under
// an explicit id keeps its counter through a merge that would prune it, so
// its next event never compares as older than its own earlier events.
func TestEmit_ExplicitAuthorCounterSurvivesPrune(t *testing.T) {
	t.Parallel()
	s, raw := emitTestStore(t)
	for i := 0; i < 5; i++ {
		emitOne(t, s, raw, "agent-x")
	}
	observeAuthorsViaApply(t, s, 250)
	require.Equal(t, int64(5), storedVC(t, raw)["agent-x"], "explicit author kept by merge")
	ev := emitOne(t, s, raw, "agent-x")
	require.Equal(t, int64(6), ev.VectorClock["agent-x"], "counter continues, never restarts")
	require.LessOrEqual(t, len(ev.VectorClock), model.MaxVectorClockEntries)
}
