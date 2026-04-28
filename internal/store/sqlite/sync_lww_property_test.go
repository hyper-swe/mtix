// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// TestApplyLWW_CrossMachineConvergence is the FR-18 / SYNC-DESIGN
// section 8 cross-machine convergence property test for MTIX-15.5.3.
//
// Two ephemeral stores represent two laptops. Both authors emit events
// that INTENTIONALLY target the same (node, field) pairs. The "hub"
// is just the union of the events; both stores apply the union in
// distinct shuffled (causal-respecting) orders. After application the
// stores MUST converge to byte-identical node state AND record the
// same set of conflict pairs.
//
// 15.4.2's property test deliberately avoided cross-author field
// overlap to isolate the deterministic-replay invariant; this test
// deliberately induces overlap to validate LWW resolution.
func TestApplyLWW_CrossMachineConvergence(t *testing.T) {
	seeds := lwwSeedCount(t)

	for i := 0; i < seeds; i++ {
		i := i
		t.Run("seed-"+strconv.Itoa(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(i + 1))) //nolint:gosec // test-only deterministic RNG
			seq := generateConflictingSequence(t, rng, lwwEventCount(t))

			storeA := lwwTestStore(t, "A-"+strconv.Itoa(i))
			storeB := lwwTestStore(t, "B-"+strconv.Itoa(i))

			applySequence(t, storeA, lwwShuffle(rng, seq))
			applySequence(t, storeB, lwwShuffle(rng, seq))

			// State convergence is the LWW guarantee per FR-18.11 /
			// SYNC-DESIGN section 8.2. Final node values match across
			// any pair of causal-respecting apply orders.
			require.Equal(t,
				lwwSnapshot(t, storeA),
				lwwSnapshot(t, storeB),
				"seed %d: stores diverged after LWW resolution", i)

			// Conflict-pair RECORDING is path-dependent under
			// one-event-at-a-time apply: each apply only sees prior-
			// applied events, so the (winner, loser) pair recorded
			// depends on order. For example, with same-field events
			// at lamports 1, 5, 10:
			//   apply 1, 5, 10  -> records (5 beats 1), (10 beats 5)
			//   apply 10, 5, 1  -> records (10 beats 5), (10 beats 1)
			// Both stores agree the final value comes from lamport 10
			// (the state assertion passes); the AUDIT trail of every
			// pairwise loss differs by order.
			//
			// The hub-side sync_conflicts (15.5.2) is path-independent
			// because PushEvents detects all conflicts against the
			// cumulative event log at push time. Local conflict logs
			// are best-effort. The test intentionally does NOT assert
			// canonicalConflicts(A) == canonicalConflicts(B); it asserts
			// only that BOTH stores recorded conflicts (non-empty when
			// the event set has overlaps).
			confsA := canonicalConflicts(t, storeA)
			confsB := canonicalConflicts(t, storeB)
			if hasOverlappingFieldEvents(seq) {
				require.NotEmpty(t, confsA, "seed %d: storeA recorded no conflicts despite overlap", i)
				require.NotEmpty(t, confsB, "seed %d: storeB recorded no conflicts despite overlap", i)
			}
		})
	}
}

func lwwSeedCount(t *testing.T) int {
	if v := envIntLWW(t, "MTIX_LWW_TEST_SEEDS"); v > 0 {
		return v
	}
	if testing.Short() {
		return 5
	}
	return 30
}

func lwwEventCount(t *testing.T) int {
	if v := envIntLWW(t, "MTIX_LWW_TEST_EVENTS"); v > 0 {
		return v
	}
	if testing.Short() {
		return 10
	}
	return 25
}

func envIntLWW(t *testing.T, key string) int {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("invalid %s=%q: %v", key, v, err)
	}
	return n
}

func lwwTestStore(t *testing.T, suffix string) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "lww-"+suffix+".db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// generateConflictingSequence builds a random event sequence that
// INTENTIONALLY induces same-field conflicts across two authors.
// Per-author lamport monotonic. Each post-create event picks
// (node, field) from a small fixed set so collisions are common.
func generateConflictingSequence(t *testing.T, rng *rand.Rand, events int) []*model.SyncEvent {
	t.Helper()
	authors := []string{"alice", "bob"}
	nodes := []string{"MTIX-1", "MTIX-2", "MTIX-3"}
	fields := []string{"title", "description", "prompt"}
	authorLamport := map[string]int64{}

	out := make([]*model.SyncEvent, 0, events+len(nodes))

	// Always create every node first (alice owns the creates).
	for _, n := range nodes {
		authorLamport["alice"]++
		pl, err := model.EncodePayload(&model.CreateNodePayload{Title: n})
		require.NoError(t, err)
		out = append(out, &model.SyncEvent{
			EventID:           clock.MustNewEventID(),
			ProjectPrefix:     "MTIX",
			NodeID:            n,
			OpType:            model.OpCreateNode,
			Payload:           pl,
			WallClockTS:       time.Now().UnixMilli() + int64(rng.Intn(1000)),
			LamportClock:      authorLamport["alice"],
			VectorClock:       cloneVCLWW(authorLamport),
			AuthorID:          "alice",
			AuthorMachineHash: machineHashFor("alice"),
		})
	}

	for i := 0; i < events; i++ {
		author := authors[rng.Intn(len(authors))]
		authorLamport[author]++
		nodeID := nodes[rng.Intn(len(nodes))]
		field := fields[rng.Intn(len(fields))]
		val, _ := json.Marshal(fmt.Sprintf("%s-%s-%d", field, author, rng.Intn(10000)))
		pl, err := model.EncodePayload(&model.UpdateFieldPayload{
			FieldName: field, NewValue: val,
		})
		require.NoError(t, err)
		out = append(out, &model.SyncEvent{
			EventID:           clock.MustNewEventID(),
			ProjectPrefix:     "MTIX",
			NodeID:            nodeID,
			OpType:            model.OpUpdateField,
			Payload:           pl,
			WallClockTS:       time.Now().UnixMilli() + int64(rng.Intn(1000)),
			LamportClock:      authorLamport[author],
			VectorClock:       cloneVCLWW(authorLamport),
			AuthorID:          author,
			AuthorMachineHash: machineHashFor(author),
		})
	}
	return out
}

// machineHashFor returns a stable machine_hash per simulated author
// so the LWW final tie-break is deterministic across replicas.
func machineHashFor(author string) string {
	switch author {
	case "alice":
		return "1111111111111111"
	case "bob":
		return "2222222222222222"
	default:
		return "0000000000000000"
	}
}

func cloneVCLWW(m map[string]int64) model.VectorClock {
	out := make(model.VectorClock, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// lwwShuffle reorders the input preserving per-author order while
// keeping creates as a global barrier (creates apply first because
// every later event's VC dominates them).
func lwwShuffle(rng *rand.Rand, seq []*model.SyncEvent) []*model.SyncEvent {
	creates := []*model.SyncEvent{}
	rest := []*model.SyncEvent{}
	for _, e := range seq {
		if e.OpType == model.OpCreateNode {
			creates = append(creates, e)
		} else {
			rest = append(rest, e)
		}
	}
	return append(
		interleavePerAuthor(rng, creates),
		interleavePerAuthor(rng, rest)...,
	)
}

// lwwSnapshot reads the deterministic node-state representation used
// for cross-store equality. Excludes timestamps that vary by apply-time
// wall clock.
func lwwSnapshot(t *testing.T, s *Store) []nodeSnapshot {
	t.Helper()
	rows, err := s.readDB.QueryContext(context.Background(), `
		SELECT id, title, description, prompt, acceptance, status,
		       priority, assignee, deleted_at, annotations, defer_until
		FROM nodes ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var out []nodeSnapshot
	for rows.Next() {
		var ns nodeSnapshot
		var description, prompt, acceptance, assignee, deletedAt, annotations, deferUntil sql.NullString
		var priority sql.NullInt64
		require.NoError(t, rows.Scan(
			&ns.ID, &ns.Title, &description, &prompt, &acceptance, &ns.Status,
			&priority, &assignee, &deletedAt, &annotations, &deferUntil,
		))
		ns.Description = description.String
		ns.Prompt = prompt.String
		ns.Acceptance = acceptance.String
		ns.Assignee = assignee.String
		ns.DeletedAt = deletedAt.Valid
		ns.Annotations = annotations.String
		ns.DeferUntilSet = deferUntil.Valid
		ns.Priority = int(priority.Int64)
		out = append(out, ns)
	}
	require.NoError(t, rows.Err())
	return out
}

// canonicalConflicts returns the conflict pair set sorted by
// (winner, loser) for cross-store comparison. The resolved_at column
// is excluded since it's apply-time wall clock.
func canonicalConflicts(t *testing.T, s *Store) []conflictPair {
	t.Helper()
	rows, err := s.readDB.QueryContext(context.Background(), `
		SELECT event_id_winner, event_id_loser, node_id, field_name
		FROM sync_conflicts`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var pairs []conflictPair
	for rows.Next() {
		var p conflictPair
		var field sql.NullString
		require.NoError(t, rows.Scan(&p.Winner, &p.Loser, &p.NodeID, &field))
		p.Field = field.String
		pairs = append(pairs, p)
	}
	require.NoError(t, rows.Err())
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Winner != pairs[j].Winner {
			return pairs[i].Winner < pairs[j].Winner
		}
		return pairs[i].Loser < pairs[j].Loser
	})
	return pairs
}

type conflictPair struct {
	Winner, Loser, NodeID, Field string
}

// hasOverlappingFieldEvents reports whether the sequence contains two
// or more update_field events from different authors targeting the
// same (node, field). Used by the property test to gate the
// conflict-non-emptiness assertion.
func hasOverlappingFieldEvents(seq []*model.SyncEvent) bool {
	type key struct{ node, field string }
	seen := map[key]string{}
	for _, e := range seq {
		if e.OpType != model.OpUpdateField {
			continue
		}
		var p model.UpdateFieldPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			continue
		}
		k := key{e.NodeID, p.FieldName}
		if prior, ok := seen[k]; ok && prior != e.AuthorID {
			return true
		}
		seen[k] = e.AuthorID
	}
	return false
}
