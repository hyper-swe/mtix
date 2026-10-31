// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

const (
	uidSelf  = "0190a000-0000-7000-8000-000000000001"
	uidParen = "0190a000-0000-7000-8000-000000000002"
	uidGrand = "0190a000-0000-7000-8000-000000000003"
)

func backfillUID(t *testing.T) string {
	t.Helper()
	u, err := model.NewBackfillUID()
	assert.NoError(t, err)
	return u
}

func ticketOf(uid string, ancestors []string, paths []string, match []bool) func(obj) {
	anc := make([]any, len(ancestors))
	for i, a := range ancestors {
		anc[i] = a
	}
	p := make([]any, len(paths))
	for i, s := range paths {
		p[i] = s
	}
	m := make([]any, len(match))
	for i, b := range match {
		m[i] = b
	}
	return set("ticket", obj{"uid": uid, "ancestors": anc, "paths": p, "path_match": m, "truncated": false})
}

func TestFold_TicketDeleted_Predicate(t *testing.T) {
	bf := backfillUID(t)
	std := ticketOf(uidSelf, []string{uidParen, uidGrand}, []string{"P-1.2.3", "P-1.2", "P-1"}, []bool{true, true, true})
	noPath := ticketOf(uidSelf, []string{uidParen, uidGrand}, []string{"P-1.2.3", "P-1.2", "P-1"}, []bool{false, false, false})
	tests := []struct {
		name       string
		mod        func(obj)
		deletes    []decisionfold.Delete
		deleted    bool
		byPathOnly bool
	}{
		{"no deletes", std, nil, false, false},
		{"own uid", std, []decisionfold.Delete{{UID: uidSelf, NodeID: "P-9"}}, true, false},
		{"own uid with cascade", std, []decisionfold.Delete{{UID: uidSelf, Cascade: true}}, true, false},
		{"parent without cascade", std, []decisionfold.Delete{{UID: uidParen, NodeID: "P-1.2"}}, false, false},
		{"parent with cascade", std, []decisionfold.Delete{{UID: uidParen, Cascade: true}}, true, false},
		{"grandparent with cascade", std, []decisionfold.Delete{{UID: uidGrand, Cascade: true}}, true, false},
		{"legacy delete of the parent counts as self only", std, []decisionfold.Delete{{UID: uidParen, NodeID: "P-1.2"}}, false, false},
		{"unrelated uid", std, []decisionfold.Delete{{UID: "0190a000-0000-7000-8000-0000000000ff", NodeID: "P-77"}}, false, false},
		{"gc purge: missing link matched by path", ticketOf(uidSelf, []string{""}, []string{"P-1.2.3", "P-1.2"}, []bool{true, true}),
			[]decisionfold.Delete{{NodeID: "P-1.2", Cascade: true}}, true, true},
		{"path match of a deleted ancestor without cascade", std, []decisionfold.Delete{{NodeID: "P-1.2"}}, false, false},
		{"path match with payload path", std, []decisionfold.Delete{{NodeID: "P-other", Path: "P-1.2.3"}}, true, true},
		{"payload path wins over node id", std, []decisionfold.Delete{{NodeID: "P-1.2.3", Path: "P-else"}}, false, false},
		{"path match only for the node id when no payload path", std, []decisionfold.Delete{{NodeID: "P-1.2.3"}}, true, true},
		{"divergent backfill uid on the delete matched by path", std,
			[]decisionfold.Delete{{UID: bf, NodeID: "P-1.2.3"}}, true, true},
		{"divergent backfill uid on the ticket matched by path",
			ticketOf(bf, nil, []string{"P-1.2.3"}, []bool{true}), []decisionfold.Delete{{UID: uidParen, NodeID: "P-1.2.3"}}, true, true},
		{"different real uids are not matched by path", std, []decisionfold.Delete{{UID: uidParen, NodeID: "P-1.2.3"}}, false, false},
		{"path_match false blocks a path match", noPath, []decisionfold.Delete{{NodeID: "P-1.2.3"}}, false, false},
		{"empty ticket uid matched by path", ticketOf("", nil, []string{"P-1.2.3"}, []bool{true}),
			[]decisionfold.Delete{{UID: uidParen, NodeID: "P-1.2.3"}}, true, true},
		{"empty uids never match each other", ticketOf("", nil, []string{"P-x"}, []bool{false}), []decisionfold.Delete{{NodeID: "P-y"}}, false, false},
		{"empty path never matches", ticketOf(uidSelf, nil, []string{""}, []bool{true}), []decisionfold.Delete{{UID: uidParen}}, false, false},
		{"uid match beats a path match", std, []decisionfold.Delete{{NodeID: "P-1.2.3"}, {UID: uidSelf}}, true, false},
		{"uid match in an ancestor beats path matches", std,
			[]decisionfold.Delete{{NodeID: "P-1.2.3"}, {UID: uidGrand, Cascade: true}}, true, false},
		{"path false positive is flagged", std, []decisionfold.Delete{{UID: "", NodeID: "P-1.2.3", Path: "P-1.2.3"}}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := request(t, "r1", tt.mod)
			c := stable(t, 22, "r1", evs{r, answer(t, "a1", alice, "r1")}, tt.deletes)
			assert.Equal(t, tt.deleted, c.HasFlag(decisionfold.FlagTicketDeleted))
			assert.Equal(t, tt.byPathOnly, c.HasFlag(decisionfold.FlagTicketMatchedByPath))
			if tt.deleted {
				assert.Equal(t, decisionfold.StateInvalid, c.State)
				assert.Equal(t, decisionfold.ReasonTicketDeleted, c.InvalidReason)
			} else {
				assert.Equal(t, decisionfold.StateAnswered, c.State)
			}
		})
	}
}

func TestFold_TicketMatchedByPath_IsIndependentOfDeleteOrder(t *testing.T) {
	r := request(t, "r1", ticketOf(uidSelf, []string{uidParen}, []string{"P-1.2", "P-1"}, []bool{true, true}))
	deletes := []decisionfold.Delete{
		{NodeID: "P-1.2"}, {UID: uidParen, Cascade: true}, {UID: "x"}, {NodeID: "P-1", Cascade: true},
	}
	want := card("r1", evs{r}, deletes...)
	assert.True(t, want.HasFlag(decisionfold.FlagTicketDeleted))
	assert.False(t, want.HasFlag(decisionfold.FlagTicketMatchedByPath), "an ancestor uid matches, so the match is by uid")
	for _, perm := range [][]int{{3, 2, 1, 0}, {1, 0, 3, 2}, {2, 0, 1, 3}, {0, 3, 1, 2}} {
		shuffled := make([]decisionfold.Delete, len(deletes))
		for i, p := range perm {
			shuffled[i] = deletes[p]
		}
		assert.Equal(t, want, card("r1", evs{r}, shuffled...), "order %v", perm)
	}
}

func TestFold_LocalUndelete_HasNoEffect(t *testing.T) {
	// The fold takes no node rows. A restore cannot reach it: no other op
	// type becomes a delete, so the delete set stays as the events made it.
	for _, op := range model.AllOpTypes {
		if op == model.OpDelete {
			continue
		}
		_, ok := decisionfold.DeleteFromSync(model.SyncEvent{OpType: op, UID: uidSelf, NodeID: "P-1"})
		assert.False(t, ok, string(op))
	}
	r := request(t, "r1")
	d := decisionfold.Delete{UID: "t-1", NodeID: "P-t-1"}
	assert.Equal(t, decisionfold.StateInvalid, card("r1", evs{r}, d).State)
	assert.Equal(t, decisionfold.StateInvalid, card("r1", evs{r}, d, d).State)
}
