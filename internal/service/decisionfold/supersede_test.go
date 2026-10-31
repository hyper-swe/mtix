// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

func keyed(key string, sup ...string) func(obj) {
	return func(o obj) {
		o["key"] = key
		if len(sup) > 0 {
			l := make([]any, len(sup))
			for i, s := range sup {
				l[i] = s
			}
			o["supersedes"] = l
		}
	}
}

func superseding(sup ...string) func(obj) {
	return func(o obj) {
		l := make([]any, len(sup))
		for i, s := range sup {
			l[i] = s
		}
		o["supersedes"] = l
	}
}

func TestFold_Supersede_Rules(t *testing.T) {
	tests := []struct {
		name   string
		events evs
		old    decisionfold.State
		newer  decisionfold.State
	}{
		{"same key", evs{request(t, "r1", keyed("k")), request(t, "r2", keyed("k", "r1"))},
			decisionfold.StateSuperseded, decisionfold.StateOpen},
		{"keyless on the same ticket", evs{request(t, "r1"), request(t, "r2", superseding("r1"))},
			decisionfold.StateSuperseded, decisionfold.StateOpen},
		{"keyless on another ticket", evs{request(t, "r1"), request(t, "r2", superseding("r1"), set("ticket", ticket("t-2")))},
			decisionfold.StateOpen, decisionfold.StateOpen},
		{"different keys", evs{request(t, "r1", keyed("a")), request(t, "r2", keyed("b", "r1"))},
			decisionfold.StateOpen, decisionfold.StateOpen},
		{"key against keyless", evs{request(t, "r1", keyed("a")), request(t, "r2", superseding("r1"))},
			decisionfold.StateOpen, decisionfold.StateOpen},
		{"another asker", evs{request(t, "r1", keyed("k")),
			ev(t, "r2", "agent-2", alice, 1, reqObj("k-r2", keyed("k", "r1")))},
			decisionfold.StateOpen, decisionfold.StateOpen},
		{"the newer request is malformed", evs{request(t, "r1", keyed("k")),
			request(t, "r2", keyed("k", "r1"), set("question", ""))},
			decisionfold.StateOpen, decisionfold.StateInvalid},
		{"the newer request is unsupported", evs{request(t, "r1", keyed("k")),
			request(t, "r2", keyed("k", "r1"), set("answerable_by", "robot"))},
			decisionfold.StateOpen, decisionfold.StateInvalid},
		{"the newer request is on a deleted ticket is invalid and supersedes nothing",
			evs{request(t, "r1", keyed("k")), request(t, "r2", keyed("k", "r1"), set("ticket", ticket("t-gone")))},
			decisionfold.StateOpen, decisionfold.StateInvalid},
		{"a request naming itself", evs{request(t, "r1", keyed("k", "r1")), request(t, "r2", set("ticket", ticket("t-2")))},
			decisionfold.StateOpen, decisionfold.StateOpen},
		{"names an unknown request", evs{request(t, "r1", keyed("k")), request(t, "r2", keyed("k", "ghost"))},
			decisionfold.StateOpen, decisionfold.StateOpen},
	}
	deletes := []decisionfold.Delete{{UID: "t-gone", NodeID: "P-t-gone"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.old, stable(t, 11, "r1", tt.events, deletes).State)
			assert.Equal(t, tt.newer, stable(t, 11, "r2", tt.events, deletes).State)
		})
	}
}

func TestFold_Supersede_ListsSupersedersAndHonoursHigherRules(t *testing.T) {
	r1 := request(t, "r1", keyed("k"))
	r2 := request(t, "r2", keyed("k", "r1"))
	r3 := request(t, "r3", keyed("k", "r1"))
	c := stable(t, 12, "r1", evs{r1, r2, r3}, nil)
	assert.Equal(t, []string{"r2", "r3"}, c.SupersededBy)
	t.Run("applied wins over superseded", func(t *testing.T) {
		evsApplied := evs{r1, r2, answer(t, "a1", alice, "r1"), outcome(t, "o1", "r1", "a1", "applied")}
		assert.Equal(t, decisionfold.StateApplied, stable(t, 12, "r1", evsApplied, nil).State)
	})
	t.Run("withdrawn wins over superseded", func(t *testing.T) {
		assert.Equal(t, decisionfold.StateWithdrawn, stable(t, 12, "r1", evs{r1, r2, withdraw(t, "w1", asker, "r1")}, nil).State)
	})
	t.Run("superseded wins over answered and conflicted", func(t *testing.T) {
		assert.Equal(t, decisionfold.StateSuperseded, stable(t, 12, "r1", evs{r1, r2, answer(t, "a1", alice, "r1")}, nil).State)
	})
	t.Run("a withdrawn newer request still supersedes", func(t *testing.T) {
		assert.Equal(t, decisionfold.StateSuperseded, stable(t, 12, "r1", evs{r1, r2, withdraw(t, "w1", asker, "r2")}, nil).State)
	})
}

func TestFold_Supersede_Cycles(t *testing.T) {
	t.Run("two requests superseding each other", func(t *testing.T) {
		evs2 := evs{request(t, "r1", keyed("k", "r2")), request(t, "r2", keyed("k", "r1"))}
		for _, id := range []string{"r1", "r2"} {
			c := stable(t, 13, id, evs2, nil)
			assert.Equal(t, decisionfold.StateOpen, c.State, id)
			assert.Equal(t, []decisionfold.Flag{decisionfold.FlagKeyConflict}, c.Flags, id)
			assert.Empty(t, c.SupersededBy)
		}
	})
	t.Run("a longer cycle", func(t *testing.T) {
		cyc := evs{request(t, "r1", keyed("k", "r2")), request(t, "r2", keyed("k", "r3")), request(t, "r3", keyed("k", "r1"))}
		for _, id := range []string{"r1", "r2", "r3"} {
			c := stable(t, 13, id, cyc, nil)
			assert.Equal(t, decisionfold.StateOpen, c.State, id)
			assert.True(t, c.HasFlag(decisionfold.FlagKeyConflict), id)
		}
	})
	t.Run("a request outside the cycle still supersedes a member", func(t *testing.T) {
		all := evs{request(t, "r1", keyed("k", "r2")), request(t, "r2", keyed("k", "r1")), request(t, "r3", keyed("k", "r1"))}
		c := stable(t, 13, "r1", all, nil)
		assert.Equal(t, decisionfold.StateSuperseded, c.State)
		assert.Equal(t, []string{"r3"}, c.SupersededBy)
		assert.True(t, c.HasFlag(decisionfold.FlagKeyConflict))
		assert.Equal(t, decisionfold.StateOpen, stable(t, 13, "r2", all, nil).State)
	})
	t.Run("a chain is not a cycle", func(t *testing.T) {
		chain := evs{request(t, "r1", keyed("k")), request(t, "r2", keyed("k", "r1")), request(t, "r3", keyed("k", "r2"))}
		assert.Equal(t, decisionfold.StateSuperseded, stable(t, 13, "r1", chain, nil).State)
		assert.Equal(t, decisionfold.StateSuperseded, stable(t, 13, "r2", chain, nil).State)
		c := stable(t, 13, "r3", chain, nil)
		assert.Equal(t, decisionfold.StateOpen, c.State)
		assert.Empty(t, c.Flags)
	})
}

func TestFold_ConcurrentReasks_BothStayOpenWithKeyConflict(t *testing.T) {
	r1, r2 := request(t, "r1", keyed("k")), request(t, "r2", keyed("k"))
	for _, id := range []string{"r1", "r2"} {
		c := stable(t, 14, id, evs{r1, r2}, nil)
		assert.Equal(t, decisionfold.StateOpen, c.State)
		assert.Equal(t, []decisionfold.Flag{decisionfold.FlagKeyConflict}, c.Flags)
	}
	t.Run("an answered peer clears the conflict on neither card", func(t *testing.T) {
		all := evs{r1, r2, answer(t, "a1", alice, "r1")}
		assert.Empty(t, stable(t, 14, "r2", all, nil).Flags)
		assert.Empty(t, stable(t, 14, "r1", all, nil).Flags)
	})
	t.Run("different askers with the same key do not conflict", func(t *testing.T) {
		other := ev(t, "r2", "agent-2", alice, 1, reqObj("k-r2", keyed("k")))
		assert.Empty(t, stable(t, 14, "r1", evs{r1, other}, nil).Flags)
	})
	t.Run("keyless requests do not conflict", func(t *testing.T) {
		assert.Empty(t, stable(t, 14, "r1", evs{request(t, "r1"), request(t, "r2")}, nil).Flags)
	})
}
