// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestDecisionTicketDeleted_IdentityPathEligibilityAndCascade(t *testing.T) {
	const backfill = "aaaaaaaa-aaaa-8bf1-aaaa-aaaaaaaaaaaa"
	for _, tc := range []struct {
		name, uid, duid, path, node               string
		ancestor, cascade, allow, deleted, byPath bool
	}{
		{"self uid", "u", "u", "other", "", false, false, false, true, false},
		{"ancestor legacy", "u", "u", "", "P", true, false, true, false, false},
		{"ancestor cascade", "u", "u", "", "", true, true, false, true, false},
		{"legacy path", "u", "", "", "P", false, false, true, true, true},
		{"payload over renamed envelope", "u", "", "P", "Q", false, false, true, true, true},
		{"envelope cannot override payload", "u", "", "Q", "P", false, false, true, false, false},
		{"disabled", "u", "", "P", "", false, false, false, false, false},
		{"strong uid mismatch", "u", "v", "P", "", false, false, true, false, false},
		{"backfill delete", "u", backfill, "P", "", false, false, true, true, true},
		{"backfill ticket", backfill, "v", "P", "", false, false, true, true, true},
		{"gc missing ancestor", "", "v", "P", "", true, true, true, true, true},
		{"empty uids different paths", "", "", "Q", "", true, true, true, false, false},
		{"ancestor by path needs cascade", "u", "", "P", "", true, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ticket := model.DecisionTicket{UID: tc.uid, Paths: []string{"P"}, PathMatch: []bool{tc.allow}}
			if tc.ancestor {
				ticket = model.DecisionTicket{UID: "child", Ancestors: []string{tc.uid}, Paths: []string{"P.1", "P"}, PathMatch: []bool{true, tc.allow}}
			}
			d := DecisionDelete{UID: tc.duid, NodeID: tc.node, Cascade: tc.cascade}
			if tc.path != "" {
				d.Path = &tc.path
			}
			deleted, byPath := DecisionTicketDeleted(ticket, []DecisionDelete{d})
			require.Equal(t, tc.deleted, deleted)
			require.Equal(t, tc.byPath, byPath)
		})
	}
}

func TestFoldDecisions_DeletionPrecedesPinAndUIDOverridesAllPathMatches(t *testing.T) {
	r := foldRequest("r")
	r.Payload.Ticket.Ancestors = []string{"parent"}
	r.Payload.Ticket.Paths = append(r.Payload.Ticket.Paths, "P")
	r.Payload.Ticket.PathMatch = append(r.Payload.Ticket.PathMatch, true)
	events := []DecisionFoldEvent{r, foldAnswer("a", "yes"), foldOutcome("o", "applied", "a")}
	byPath, byUID := DecisionDelete{NodeID: "T-1"}, DecisionDelete{UID: "parent", Cascade: true}
	for _, deletes := range [][]DecisionDelete{{byPath}, {byPath, byUID}, {byUID, byPath}} {
		c := FoldDecisions(events, deletes)["r"]
		require.EqualValues(t, "invalid", c.State)
		require.Equal(t, "ticket_deleted", c.InvalidReason)
		require.True(t, c.TicketDeleted)
		require.Equal(t, len(deletes) == 1, c.TicketMatchedByPath)
		require.Empty(t, c.PinnedAnswerID)
	}
}

// Protocol 1 emits no restore or purge event. The same journal snapshot must
// close the card even on a peer that retained its answer or lost the parent row.
func TestFoldDecisions_LocalUndeleteAndGCPreserveJournalDeletion(t *testing.T) {
	r := foldRequest("r")
	r.Payload.Ticket = model.DecisionTicket{UID: "child", Ancestors: []string{""}, Paths: []string{"TEST-1.1", "TEST-1"}, PathMatch: []bool{true, true}}
	path := "TEST-1"
	deletes := []DecisionDelete{{UID: "parent", Path: &path, Cascade: true}}
	onRestoredReplica := FoldDecisions([]DecisionFoldEvent{r, foldAnswer("a", "after restore")}, deletes)
	onPurgedReplica := FoldDecisions([]DecisionFoldEvent{r}, deletes)
	require.Equal(t, onRestoredReplica, onPurgedReplica)
	require.EqualValues(t, "invalid", onPurgedReplica["r"].State)
	require.True(t, onPurgedReplica["r"].TicketMatchedByPath)
}
