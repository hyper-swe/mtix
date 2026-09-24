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
	"github.com/stretchr/testify/require"
)

// Workflow permutation property for MTIX-95.10 (ADR-006 D2; review F-28).
//
// For a set of workflow events on one node, every arrival order must leave
// the same status and closed_at, and they must be the winner's: the event with
// the highest (lamport_clock, event_id). A terminal closed_at is the winner's
// wall_clock_ts in whole seconds, never the apply time. When the winner is a
// claim or an unclaim, the assignee it writes converges too.
//
// Each permutation is applied, in one pull-style transaction, to its own node
// of one store; per-node event ids keep the scenario's relative id order, so
// every node sees the same contest.

// wfEvent is one workflow event of a scenario, before it is bound to a node.
type wfEvent struct {
	id      string // relative order decides Lamport ties
	lamport int64
	wall    time.Time
	op      model.OpType
	payload any
	status  model.Status // the status the event writes
	agent   string       // claim: the assignee it writes
}

// wfAt returns the scenario wall clock plus sec seconds; distinct offsets keep
// terminal winners apart.
func wfAt(sec int) time.Time {
	return time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
}

func wfClaim(id string, lamport int64, wallSec int, agent string) wfEvent {
	return wfEvent{id: id, lamport: lamport, wall: wfAt(wallSec),
		op: model.OpClaim, payload: &model.ClaimPayload{AgentID: agent},
		status: model.StatusInProgress, agent: agent}
}

func wfUnclaim(id string, lamport int64, wallSec int) wfEvent {
	return wfEvent{id: id, lamport: lamport, wall: wfAt(wallSec),
		op: model.OpUnclaim, payload: &model.UnclaimPayload{}, status: model.StatusOpen}
}

func wfDefer(id string, lamport int64, wallSec int) wfEvent {
	return wfEvent{id: id, lamport: lamport, wall: wfAt(wallSec),
		op: model.OpDefer, payload: &model.DeferPayload{Reason: "later"}, status: model.StatusDeferred}
}

func wfMove(id string, lamport int64, wallSec int, from, to model.Status) wfEvent {
	return wfEvent{id: id, lamport: lamport, wall: wfAt(wallSec),
		op: model.OpTransitionStatus, payload: &model.TransitionStatusPayload{From: from, To: to},
		status: to}
}

// wfFixedScenarios are hand-built contests; each hits one hazard.
func wfFixedScenarios() map[string][]wfEvent {
	return map[string][]wfEvent{
		"claim race, then done and unclaim tie at the top": {
			wfClaim("e1", 2, 1, "agent-a"), wfClaim("e2", 2, 2, "agent-b"),
			wfMove("e3", 3, 3, model.StatusInProgress, model.StatusDone), wfUnclaim("e4", 3, 4),
			wfDefer("e0", 1, 0),
		},
		"late claim and defer below a done": {
			wfClaim("e1", 2, 1, "agent-a"), wfMove("e5", 4, 50, model.StatusInProgress, model.StatusDone),
			wfClaim("e3", 3, 60, "agent-b"), wfDefer("e2", 1, 70), wfUnclaim("e4", 3, 80),
		},
		"two terminal events: closed_at comes from the winner, not the earliest": {
			wfMove("e1", 3, 900, model.StatusInProgress, model.StatusDone),
			wfMove("e2", 5, 10, model.StatusOpen, model.StatusCancelled),
			wfClaim("e3", 2, 5, "agent-a"), wfMove("e4", 4, 20, model.StatusDone, model.StatusOpen),
		},
		"invalidation wins over a concurrent reopen and a restart": {
			wfMove("e1", 2, 1, model.StatusInProgress, model.StatusDone),
			wfMove("e3", 3, 30, model.StatusDone, model.StatusInvalidated),
			wfMove("e2", 3, 40, model.StatusDone, model.StatusOpen),
			wfMove("e0", 3, 50, model.StatusOpen, model.StatusInProgress),
			wfClaim("e4", 1, 60, "agent-c"),
		},
	}
}

// wfRandomEvent draws one workflow event from the shapes the local code emits.
func wfRandomEvent(rng *rand.Rand, id string) wfEvent {
	lamport := int64(rng.Intn(3) + 1) // a narrow range forces Lamport ties
	wall := rng.Intn(3600)
	switch rng.Intn(9) {
	case 0:
		return wfClaim(id, lamport, wall, "agent-"+strconv.Itoa(rng.Intn(3)))
	case 1:
		return wfUnclaim(id, lamport, wall)
	case 2:
		return wfDefer(id, lamport, wall)
	case 3:
		return wfMove(id, lamport, wall, model.StatusInProgress, model.StatusDone)
	case 4:
		return wfMove(id, lamport, wall, model.StatusOpen, model.StatusCancelled)
	case 5:
		return wfMove(id, lamport, wall, model.StatusDone, model.StatusInvalidated)
	case 6:
		return wfMove(id, lamport, wall, model.StatusInProgress, model.StatusBlocked)
	case 7:
		return wfMove(id, lamport, wall, model.StatusBlocked, model.StatusInProgress)
	default:
		return wfMove(id, lamport, wall, model.StatusDone, model.StatusOpen)
	}
}

// wfRandomScenarios draws seeded four-event contests (24 orders each).
func wfRandomScenarios() map[string][]wfEvent {
	seeds := 20
	if testing.Short() {
		seeds = 6
	}
	out := make(map[string][]wfEvent, seeds)
	for seed := 1; seed <= seeds; seed++ {
		rng := rand.New(rand.NewSource(int64(seed))) //nolint:gosec // test-only deterministic RNG
		evs := make([]wfEvent, 4)
		for i := range evs {
			evs[i] = wfRandomEvent(rng, "r"+strconv.Itoa(rng.Intn(1000)+1000)+"-"+strconv.Itoa(i))
		}
		out["seed-"+strconv.Itoa(seed)] = evs
	}
	return out
}

// wfPermutations returns every ordering of the indexes 0..n-1.
func wfPermutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, p := range wfPermutations(n - 1) {
		for pos := 0; pos <= len(p); pos++ {
			q := make([]int, 0, n)
			q = append(q, p[:pos]...)
			q = append(q, n-1)
			q = append(q, p[pos:]...)
			out = append(out, q)
		}
	}
	return out
}

// wfWinner returns the scenario event with the highest (lamport, id).
func wfWinner(evs []wfEvent) wfEvent {
	w := evs[0]
	for _, e := range evs[1:] {
		if e.lamport > w.lamport || (e.lamport == w.lamport && e.id > w.id) {
			w = e
		}
	}
	return w
}

// wfSyncEvent binds a scenario event to one node.
func wfSyncEvent(t *testing.T, nodeID string, e wfEvent) *model.SyncEvent {
	t.Helper()
	return &model.SyncEvent{
		EventID:           nodeID + "/" + e.id,
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            e.op,
		Payload:           mustEncode(t, e.payload),
		WallClockTS:       e.wall.UnixMilli(),
		LamportClock:      e.lamport,
		VectorClock:       model.VectorClock{"peer": e.lamport},
		AuthorID:          "peer",
		AuthorMachineHash: "0123456789abcdef",
	}
}

// wfApplyPermutation creates nodeID and applies evs in the order perm, in one
// transaction, the way a pull applies a batch.
func wfApplyPermutation(t *testing.T, s *Store, nodeID string, evs []wfEvent, perm []int) {
	t.Helper()
	create := &model.SyncEvent{
		EventID: nodeID + "/create", ProjectPrefix: "MTIX", NodeID: nodeID,
		OpType: model.OpCreateNode, Payload: mustEncode(t, &model.CreateNodePayload{Title: nodeID}),
		WallClockTS: wfAt(0).UnixMilli(), LamportClock: 0, VectorClock: model.VectorClock{},
		AuthorID: "peer", AuthorMachineHash: "0123456789abcdef",
	}
	ctx := context.Background()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := IdempotentApply(ctx, tx, create); err != nil {
			return err
		}
		for _, i := range perm {
			if err := IdempotentApply(ctx, tx, wfSyncEvent(t, nodeID, evs[i])); err != nil {
				return err
			}
		}
		return nil
	}))
}

// wfExpected is the (status, closed_at, assignee) the winner dictates.
// assignee is checked only when the winner writes it.
type wfExpected struct {
	status, closedAt, assignee string
	checkAssignee              bool
}

func wfExpectedFor(w wfEvent) wfExpected {
	exp := wfExpected{status: string(w.status)}
	if w.status.IsTerminal() {
		exp.closedAt = w.wall.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	switch w.op {
	case model.OpClaim:
		exp.assignee, exp.checkAssignee = w.agent, true
	case model.OpUnclaim:
		exp.checkAssignee = true
	}
	return exp
}

// TestApply_WorkflowEventPermutations_StatusAndClosedAtConverge applies every
// permutation of each scenario and checks that all of them end on the
// winner's status and closed_at (and assignee, for a claim or unclaim winner).
func TestApply_WorkflowEventPermutations_StatusAndClosedAtConverge(t *testing.T) {
	scenarios := wfFixedScenarios()
	for name, evs := range wfRandomScenarios() {
		scenarios[name] = evs
	}
	for name, evs := range scenarios {
		t.Run(name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			exp := wfExpectedFor(wfWinner(evs))
			for n, perm := range wfPermutations(len(evs)) {
				nodeID := fmt.Sprintf("MTIX-%d", n+1)
				wfApplyPermutation(t, s, nodeID, evs, perm)

				var status string
				var closedAt, assignee sql.NullString
				// The converging registers of one permuted node.
				require.NoError(t, raw.QueryRow(
					`SELECT status, closed_at, assignee FROM nodes WHERE id = ?`, nodeID,
				).Scan(&status, &closedAt, &assignee))
				require.Equal(t, exp.status, status, "order %v: status", perm)
				require.Equal(t, exp.closedAt, closedAt.String, "order %v: closed_at", perm)
				if exp.checkAssignee {
					require.Equal(t, exp.assignee, assignee.String, "order %v: assignee", perm)
				}
			}
		})
	}
}
