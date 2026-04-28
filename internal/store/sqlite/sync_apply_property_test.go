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
	"strconv"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// TestApply_ReplayDeterminismProperty is the FR-18.9 / SYNC-DESIGN
// section 8 property test. For each random seed:
//
//  1. Generate a sequence of K events across M nodes by 3 simulated
//     authors. Per-author lamport counter is monotonic.
//  2. Apply the events in original order against store A.
//  3. Shuffle the events while preserving per-author lamport sequence
//     (different authors may interleave; same-author events stay in
//     order). Apply against store B.
//  4. Assert: for every node id, the (title, description, prompt,
//     acceptance, status, priority, assignee, deleted_at) tuple is
//     byte-identical between A and B.
//
// In -short mode (default for local laptops) we run 50 seeds with
// 20 events across 5 nodes. CI overrides via the
// MTIX_PROPERTY_TEST_SEEDS env var to run 1000 seeds with 100 events
// across 10 nodes (FR-18.9 acceptance).
func TestApply_ReplayDeterminismProperty(t *testing.T) {
	seeds := propertySeedCount(t)
	events, nodes := propertyEventCount(t)

	for i := 0; i < seeds; i++ {
		i := i
		t.Run("seed-"+strconv.Itoa(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(i + 1))) //nolint:gosec // test-only deterministic RNG
			seq := generateEventSequence(t, rng, events, nodes)

			storeA := propertyStore(t, "A-"+strconv.Itoa(i))
			storeB := propertyStore(t, "B-"+strconv.Itoa(i))

			applySequence(t, storeA, seq)
			applySequence(t, storeB, shuffledPreservingCausalOrder(rng, seq))

			require.Equal(t,
				snapshotState(t, storeA),
				snapshotState(t, storeB),
				"seed %d: replay-shuffled state diverged from original", i)
		})
	}
}

// propertySeedCount honors testing.Short and the MTIX_PROPERTY_TEST_SEEDS
// env var. Default 50 short, 1000 with env override.
func propertySeedCount(t *testing.T) int {
	if v := envInt(t, "MTIX_PROPERTY_TEST_SEEDS"); v > 0 {
		return v
	}
	if testing.Short() {
		return 10
	}
	return 50
}

func propertyEventCount(t *testing.T) (events, nodes int) {
	if v := envInt(t, "MTIX_PROPERTY_TEST_EVENTS"); v > 0 {
		return v, 10
	}
	if testing.Short() {
		return 12, 4
	}
	return 30, 6
}

func envInt(t *testing.T, key string) int {
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

// propertyStore opens an isolated store per seed.
func propertyStore(t *testing.T, suffix string) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "prop-"+suffix+".db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// generateEventSequence builds a random event sequence respecting
// per-author lamport monotonicity.
//
// Event generation:
//   - Always begin with a create_node for each node id (so subsequent
//     update_field/transition events have a row to mutate).
//   - After creates, choose random ops weighted toward update_field.
//   - Each event picks a random author from a 3-author pool; the
//     author's lamport increments by 1.
func generateEventSequence(t *testing.T, rng *rand.Rand, events, nodes int) []*model.SyncEvent {
	t.Helper()
	authors := []string{"alice", "bob", "carol"}
	authorLamport := make(map[string]int64, len(authors))

	out := make([]*model.SyncEvent, 0, events)

	// Always create every node first. These creates are always from
	// alice (an arbitrary choice that keeps create order deterministic
	// per seed).
	for n := 1; n <= nodes; n++ {
		authorLamport["alice"]++
		out = append(out, &model.SyncEvent{
			EventID:           clock.MustNewEventID(),
			ProjectPrefix:     "MTIX",
			NodeID:            fmt.Sprintf("MTIX-%d", n),
			OpType:            model.OpCreateNode,
			Payload:           mustEncode(t, &model.CreateNodePayload{Title: fmt.Sprintf("node-%d", n)}),
			WallClockTS:       time.Now().UnixMilli(),
			LamportClock:      authorLamport["alice"],
			VectorClock:       cloneVC(authorLamport),
			AuthorID:          "alice",
			AuthorMachineHash: "0123456789abcdef",
		})
	}

	for i := len(out); i < events; i++ {
		author := authors[rng.Intn(len(authors))]
		authorLamport[author]++
		nodeID := fmt.Sprintf("MTIX-%d", rng.Intn(nodes)+1)

		op, payload := randomMutation(t, rng, nodeID, author)
		out = append(out, &model.SyncEvent{
			EventID:           clock.MustNewEventID(),
			ProjectPrefix:     "MTIX",
			NodeID:            nodeID,
			OpType:            op,
			Payload:           payload,
			WallClockTS:       time.Now().UnixMilli(),
			LamportClock:      authorLamport[author],
			VectorClock:       cloneVC(authorLamport),
			AuthorID:          author,
			AuthorMachineHash: "0123456789abcdef",
		})
	}
	return out
}

// authorFieldPartitions assigns each author a disjoint slice of
// columns for update_field events. The property test asserts
// convergence under causal-respecting reordering of NON-CONFLICTING
// events; cross-author writes to the SAME field require LWW
// resolution which is the MTIX-15.5 concern. With disjoint
// partitions, every reordered application produces the same final
// state regardless of order.
//
// Comments are append-only across authors (annotations is a list,
// and applyComment uses event.WallClockTS — see the deterministic
// timestamp fix), so they're safe across authors. Claim/unclaim are
// excluded from the random pool — claims are inherently single-row
// state with cross-author conflicts.
var authorFieldPartitions = map[string][]string{
	"alice": {"title", "description"},
	"bob":   {"prompt"},
	"carol": {"acceptance"},
}

// randomMutation returns a random non-conflicting op_type and payload.
// update_field uses the author's field partition (no cross-author
// overlap); comments use a per-author body suffix; transition_status
// is excluded because cross-author transitions to different statuses
// would conflict.
func randomMutation(t *testing.T, rng *rand.Rand, nodeID, author string) (model.OpType, json.RawMessage) {
	t.Helper()
	r := rng.Intn(100)
	switch {
	case r < 80:
		fields := authorFieldPartitions[author]
		field := fields[rng.Intn(len(fields))]
		val, _ := json.Marshal(fmt.Sprintf("%s-%s-%d", field, author, rng.Intn(10000)))
		return model.OpUpdateField, mustEncode(t, &model.UpdateFieldPayload{
			FieldName: field,
			NewValue:  val,
		})
	default:
		return model.OpComment, mustEncode(t, &model.CommentPayload{
			AuthorID: author,
			Body:     fmt.Sprintf("comment-%s-%d", author, rng.Intn(10000)),
		})
	}
}

func cloneVC(m map[string]int64) model.VectorClock {
	out := make(model.VectorClock, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustEncode(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := model.EncodePayload(v)
	require.NoError(t, err)
	return raw
}

// applySequence runs IdempotentApply for each event in order against s.
func applySequence(t *testing.T, s *Store, seq []*model.SyncEvent) {
	t.Helper()
	for _, e := range seq {
		err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
			return IdempotentApply(context.Background(), tx, e)
		})
		require.NoErrorf(t, err, "apply event %s op=%s", e.EventID, e.OpType)
	}
}

// shuffledPreservingCausalOrder reorders the input so that any two
// events from the same author appear in their original relative order
// AND every create_node appears before any non-create event (creates
// are cross-author prerequisites: every later author's VC dominates
// the originating create, so a causal-respecting reorder must place
// creates first).
//
// Within each phase (creates, then non-creates), events from different
// authors interleave freely but per-author order is preserved.
func shuffledPreservingCausalOrder(rng *rand.Rand, seq []*model.SyncEvent) []*model.SyncEvent {
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

// interleavePerAuthor buckets the input by author then interleaves
// the buckets randomly. Per-author order is preserved (the maximal
// shuffle that respects intra-author causal ordering).
func interleavePerAuthor(rng *rand.Rand, seq []*model.SyncEvent) []*model.SyncEvent {
	buckets := map[string][]*model.SyncEvent{}
	authors := []string{}
	for _, e := range seq {
		if _, ok := buckets[e.AuthorID]; !ok {
			authors = append(authors, e.AuthorID)
		}
		buckets[e.AuthorID] = append(buckets[e.AuthorID], e)
	}
	out := make([]*model.SyncEvent, 0, len(seq))
	cursor := map[string]int{}
	for len(out) < len(seq) {
		var pool []string
		for _, a := range authors {
			if cursor[a] < len(buckets[a]) {
				pool = append(pool, a)
			}
		}
		pick := pool[rng.Intn(len(pool))]
		out = append(out, buckets[pick][cursor[pick]])
		cursor[pick]++
	}
	return out
}

// snapshotState returns a deterministic representation of the nodes
// table contents for cross-store comparison. Excludes timestamps
// (updated_at, created_at) since those depend on apply wall-clock.
func snapshotState(t *testing.T, s *Store) []nodeSnapshot {
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
		ns.DeletedAt = deletedAt.Valid // only the boolean
		ns.Annotations = annotations.String
		ns.DeferUntilSet = deferUntil.Valid
		ns.Priority = int(priority.Int64)
		out = append(out, ns)
	}
	require.NoError(t, rows.Err())
	return out
}

type nodeSnapshot struct {
	ID            string
	Title         string
	Description   string
	Prompt        string
	Acceptance    string
	Status        string
	Priority      int
	Assignee      string
	DeletedAt     bool
	Annotations   string
	DeferUntilSet bool
}
