// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestFoldDecisions_SupersessionComponentsAndConcurrentReasks(t *testing.T) {
	a, b, c, d := foldRequest("a"), foldRequest("b"), foldRequest("c"), foldRequest("d")
	for _, e := range []DecisionFoldEvent{a, b, c, d} {
		e.Payload.Key = "gate"
	}
	a.Payload.Supersedes = []string{"b", "d"}
	b.Payload.Supersedes = []string{"c"}
	c.Payload.Supersedes = []string{"a"}
	cards := FoldDecisions([]DecisionFoldEvent{d, b, c, a}, nil)
	for _, id := range []string{"a", "b", "c"} {
		require.EqualValues(t, "open", cards[id].State)
		require.True(t, cards[id].KeyConflict)
	}
	require.EqualValues(t, "superseded", cards["d"].State)
	require.False(t, cards["d"].KeyConflict)
	d.Payload.Supersedes = []string{"b"}
	a.Payload.Supersedes = []string{"b"}
	cards = FoldDecisions([]DecisionFoldEvent{d, b, c, a}, nil)
	require.EqualValues(t, "superseded", cards["b"].State)
	require.True(t, cards["b"].KeyConflict)
	a.Payload.Supersedes = nil
	b.Payload.Supersedes = nil
	cards = FoldDecisions([]DecisionFoldEvent{b, a}, nil)
	for _, id := range []string{"a", "b"} {
		require.EqualValues(t, "open", cards[id].State)
		require.True(t, cards[id].KeyConflict)
	}
}
func TestFoldDecisions_OnlyEligibleRequestsSupersede(t *testing.T) {
	r, next := foldRequest("r"), foldRequest("next")
	alias := r
	alias.ID = "z"
	next.Payload.Supersedes = []string{"z"}
	check := func(want string) { t.Helper(); require.EqualValues(t, want, card(r, alias, next).State) }
	check("superseded")
	next.Payload.Ticket.UID = "other"
	check("open")
	r.Payload.Key, next.Payload.Key = "gate", "gate"
	check("superseded")
	next.Payload.Key = "different"
	check("open")
	next.Payload.Key = "gate"
	next.Author = "other"
	check("open")
	next.Author = "asker"
	next.Redacted = true
	check("open")
	next.Redacted = false
	cards := FoldDecisions([]DecisionFoldEvent{r, alias, next}, []DecisionDelete{{UID: "other"}})
	require.EqualValues(t, "open", cards["r"].State)
	require.EqualValues(t, "invalid", cards["next"].State)
}

// Compare every directed three-request graph to an independent reachability
// oracle: only incoming edges with no return path supersede their target.
func TestFoldDecisions_AllThreeRequestSupersedeGraphs(t *testing.T) {
	for mask := 0; mask < 64; mask++ {
		events := []DecisionFoldEvent{foldRequest("a"), foldRequest("b"), foldRequest("c")}
		var edge [3][3]bool
		bit := 0
		for i, e := range events {
			for j, target := range events {
				if i == j {
					continue
				}
				edge[i][j] = mask&(1<<bit) != 0
				if edge[i][j] {
					e.Payload.Supersedes = append(e.Payload.Supersedes, target.ID)
				}
				bit++
			}
		}
		reaches := edge
		for k := range 3 {
			for i := range 3 {
				for j := range 3 {
					reaches[i][j] = reaches[i][j] || reaches[i][k] && reaches[k][j]
				}
			}
		}
		cards := FoldDecisions(events, nil)
		for i, e := range events {
			state := "open"
			for j := range events {
				if edge[j][i] && !reaches[i][j] {
					state = "superseded"
				}
			}
			require.EqualValues(t, state, cards[e.ID].State, "mask %d request %s", mask, e.ID)
			require.Equal(t, reaches[i][i], cards[e.ID].KeyConflict, "mask %d request %s", mask, e.ID)
		}
	}
}

// Rule 1 outranks rule 4: a valid request naming an invalid one leaves it invalid.
func TestFoldDecisions_SupersedingAnInvalidRequestKeepsItInvalid(t *testing.T) {
	r, next := foldRequest("r"), foldRequest("next")
	r.Payload.Key, next.Payload.Key = "gate", "gate"
	next.Payload.Supersedes = []string{"r"}
	r.DecodeErr = model.ErrDecisionUnsupported
	cards := FoldDecisions([]DecisionFoldEvent{r, next}, nil)
	require.EqualValues(t, "invalid", cards["r"].State)
	require.Equal(t, "unsupported", cards["r"].InvalidReason)
	require.EqualValues(t, "open", cards["next"].State)
}

// Rule 7 sets the reopened flag only for open cards, so supersession clears it.
func TestFoldDecisions_SupersededCardClearsReopenedAfterRefusal(t *testing.T) {
	r, next := foldRequest("r"), foldRequest("next")
	r.Payload.Key, next.Payload.Key = "gate", "gate"
	a, b := foldAnswer("a", "yes"), foldAnswer("b", "no")
	o := foldOutcome("o", "refused", "b", "a", "b")
	before := FoldDecisions([]DecisionFoldEvent{r, a, b, o}, nil)
	require.True(t, before["r"].ReopenedAfterRefusal, "fixture must reopen after a refusal")
	next.Payload.Supersedes = []string{"r"}
	cards := FoldDecisions([]DecisionFoldEvent{r, a, b, o, next}, nil)
	require.EqualValues(t, "superseded", cards["r"].State)
	require.False(t, cards["r"].ReopenedAfterRefusal)
}
