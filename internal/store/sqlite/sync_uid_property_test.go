// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strconv"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// TestApply_UIDReplayConvergence_WithRenumber is the ADR-003 §10
// replay-convergence PROPERTY test for the UID-keyed apply path
// (MTIX-30.6). For each seed it:
//
//  1. Builds an event stream across several nodes by 3 authors, every
//     event carrying its node's stable UID (create_node self-anchors;
//     other ops carry the same uid). Per-author lamport order is
//     monotonic and is preserved under shuffling.
//  2. Applies the stream — INCLUDING a simulated renumber on one node
//     midway (the node's display_path changes; its UID is unchanged and
//     its events are NOT rewritten) — to store A in original order and to
//     store B in a causally-shuffled order.
//  3. Asserts the two stores hold BYTE-IDENTICAL node tuples keyed by uid.
//
// The renumber is the load-bearing case: because events key on uid, a
// display_path move touches ZERO events and convergence is unaffected —
// the ADR §10 claim made concrete.
func TestApply_UIDReplayConvergence_WithRenumber(t *testing.T) {
	seeds := propertySeedCount(t)
	for i := 0; i < seeds; i++ {
		i := i
		t.Run("seed-"+strconv.Itoa(i), func(t *testing.T) {
			rng := rand.New(rand.NewSource(int64(i + 1000))) //nolint:gosec // test-only deterministic RNG
			stream := buildUIDEventStream(t, rng, 5)

			storeA := propertyStore(t, "uidA-"+strconv.Itoa(i))
			storeB := propertyStore(t, "uidB-"+strconv.Itoa(i))

			// Apply to A in original order; renumber node #1 partway through.
			applyUIDStreamWithRenumber(t, storeA, stream, false, rng)
			// Apply to B causally shuffled; renumber the SAME node by uid.
			applyUIDStreamWithRenumber(t, storeB, stream, true, rng)

			require.Equal(t,
				snapshotByUID(t, storeA),
				snapshotByUID(t, storeB),
				"seed %d: uid-keyed replay diverged across shuffle+renumber", i)
		})
	}
}

// uidNode bundles a node's display path and its stable uid.
type uidNode struct {
	display string
	uid     string
}

// buildUIDEventStream creates a create_node per node (uid = create event
// id, self-anchored) followed by uid-bearing, non-conflicting mutations.
// It returns the events plus the node table so the caller can renumber.
func buildUIDEventStream(t *testing.T, rng *rand.Rand, nodes int) []*model.SyncEvent {
	t.Helper()
	authors := []string{"alice", "bob", "carol"}
	lamport := map[string]int64{}

	out := []*model.SyncEvent{}
	nodeUIDs := make([]uidNode, 0, nodes)
	for n := 1; n <= nodes; n++ {
		eventID := clock.MustNewEventID()
		lamport["alice"]++
		display := fmt.Sprintf("MTIX-%d", n)
		out = append(out, &model.SyncEvent{
			EventID:           eventID,
			ProjectPrefix:     "MTIX",
			NodeID:            display,
			UID:               eventID, // self-anchor
			OpType:            model.OpCreateNode,
			Payload:           mustEncode(t, &model.CreateNodePayload{Title: fmt.Sprintf("node-%d", n)}),
			WallClockTS:       time.Now().UnixMilli(),
			LamportClock:      lamport["alice"],
			VectorClock:       cloneVC(lamport),
			AuthorID:          "alice",
			AuthorMachineHash: "0123456789abcdef",
		})
		nodeUIDs = append(nodeUIDs, uidNode{display: display, uid: eventID})
	}

	// 24 mutations, each carrying the target node's uid, partitioned by
	// author so reordering converges without LWW.
	for i := 0; i < 24; i++ {
		author := authors[rng.Intn(len(authors))]
		lamport[author]++
		target := nodeUIDs[rng.Intn(len(nodeUIDs))]
		op, payload := randomMutation(t, rng, target.display, author)
		out = append(out, &model.SyncEvent{
			EventID:           clock.MustNewEventID(),
			ProjectPrefix:     "MTIX",
			NodeID:            target.display,
			UID:               target.uid, // key on the stable uid
			OpType:            op,
			Payload:           payload,
			WallClockTS:       time.Now().UnixMilli(),
			LamportClock:      lamport[author],
			VectorClock:       cloneVC(lamport),
			AuthorID:          author,
			AuthorMachineHash: "0123456789abcdef",
		})
	}
	return out
}

// applyUIDStreamWithRenumber applies the create events first, then
// renumbers node MTIX-1 (by uid — a display-path-only move that touches no
// events), then applies the remaining events. When shuffle is true the
// non-create events are interleaved per-author (causal order preserved).
func applyUIDStreamWithRenumber(t *testing.T, s *Store, stream []*model.SyncEvent, shuffle bool, rng *rand.Rand) {
	t.Helper()
	creates := []*model.SyncEvent{}
	rest := []*model.SyncEvent{}
	var renumberUID string
	for _, e := range stream {
		if e.OpType == model.OpCreateNode {
			creates = append(creates, e)
			if e.NodeID == "MTIX-1" {
				renumberUID = e.UID
			}
		} else {
			rest = append(rest, e)
		}
	}

	applySequence(t, s, creates)

	// Renumber the chosen node: display_path MTIX-1 -> MTIX-99, uid stable.
	// No event is emitted or rewritten — the ADR §10 invariant.
	_, err := s.writeDB.ExecContext(context.Background(),
		`UPDATE nodes SET id = ? WHERE uid = ?`, "MTIX-99", renumberUID)
	require.NoError(t, err)

	if shuffle {
		rest = interleavePerAuthor(rng, rest)
	}
	applySequence(t, s, rest)
}

// snapshotByUID returns node tuples keyed and ordered by the durable uid
// (NOT the display path, which moves under renumber), so two stores that
// renumbered to potentially different display paths still compare equal on
// the stable identity.
func snapshotByUID(t *testing.T, s *Store) map[string]nodeSnapshot {
	t.Helper()
	rows, err := s.readDB.QueryContext(context.Background(), `
		SELECT uid, title, description, prompt, acceptance, status,
		       priority, assignee, deleted_at, annotations, defer_until
		FROM nodes ORDER BY uid`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	out := map[string]nodeSnapshot{}
	for rows.Next() {
		var ns nodeSnapshot
		var uid string
		var description, prompt, acceptance, assignee, deletedAt, annotations, deferUntil sql.NullString
		var priority sql.NullInt64
		require.NoError(t, rows.Scan(
			&uid, &ns.Title, &description, &prompt, &acceptance, &ns.Status,
			&priority, &assignee, &deletedAt, &annotations, &deferUntil,
		))
		ns.ID = "" // display path intentionally excluded — uid is the key
		ns.Description = description.String
		ns.Prompt = prompt.String
		ns.Acceptance = acceptance.String
		ns.Assignee = assignee.String
		ns.DeletedAt = deletedAt.Valid
		ns.Annotations = annotations.String
		ns.DeferUntilSet = deferUntil.Valid
		ns.Priority = int(priority.Int64)
		out[uid] = ns
	}
	require.NoError(t, rows.Err())
	return out
}
