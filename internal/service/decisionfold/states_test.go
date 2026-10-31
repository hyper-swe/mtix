// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

type evs = []decisionfold.Event

func TestFold_OpenAndAnswered_Basics(t *testing.T) {
	r := request(t, "r1")
	tests := []struct {
		name      string
		events    evs
		wantState decisionfold.State
		agreeing  []string
	}{
		{"request alone is open", evs{r}, decisionfold.StateOpen, nil},
		{"one answer", evs{r, answer(t, "a1", alice, "r1")}, decisionfold.StateAnswered, []string{"a1"}},
		{"same answer repeated by one decider", evs{r, answer(t, "a1", alice, "r1"), answer(t, "a2", alice, "r1")},
			decisionfold.StateAnswered, []string{"a1", "a2"}},
		{"same answer from two deciders", evs{request(t, "r1", set("answerers", []any{bob})),
			answer(t, "a1", alice, "r1"), answer(t, "a2", bob, "r1")}, decisionfold.StateAnswered, []string{"a1", "a2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := stable(t, 1, "r1", tt.events, nil)
			assert.True(t, c.Present)
			assert.Equal(t, tt.wantState, c.State)
			assert.Equal(t, tt.agreeing, c.AgreeingAnswers)
			if len(tt.agreeing) > 0 {
				assert.Equal(t, tt.agreeing[0], c.EffectiveAnswerID, "effective answer is the minimum event id")
			}
			assert.Empty(t, c.Flags)
		})
	}
}

func TestFold_IneligibleAnswers_AreIgnored(t *testing.T) {
	agent := func(o obj) { o["decider"] = obj{"kind": "agent"} }
	anyAgent := set("answerable_by", "any")
	tests := []struct {
		name   string
		req    evs
		answer decisionfold.Event
		want   decisionfold.State
	}{
		{"by the asker", evs{request(t, "r1")}, answer(t, "a1", asker, "r1"), decisionfold.StateOpen},
		{"by a stranger", evs{request(t, "r1")}, answer(t, "a1", "mallory", "r1"), decisionfold.StateOpen},
		{"by cli", evs{request(t, "r1", set("answerers", []any{"cli"}))}, answer(t, "a1", "cli", "r1"), decisionfold.StateOpen},
		{"agent decider on a human request", evs{request(t, "r1")}, answer(t, "a1", alice, "r1", agent), decisionfold.StateOpen},
		{"agent decider on an any request", evs{request(t, "r1", anyAgent)}, answer(t, "a1", alice, "r1", agent), decisionfold.StateAnswered},
		{"unknown option", evs{request(t, "r1")}, answer(t, "a1", alice, "r1", set("option", "nope")), decisionfold.StateOpen},
		{"missing required input", evs{request(t, "r1")}, answer(t, "a1", alice, "r1", set("option", "text")), decisionfold.StateOpen},
		{"input given", evs{request(t, "r1")}, answer(t, "a1", alice, "r1", set("option", "text"), set("input_value", "go")), decisionfold.StateAnswered},
		{"whitespace-only input", evs{request(t, "r1")}, answer(t, "a1", alice, "r1", set("option", "text"), set("input_value", " \n")), decisionfold.StateOpen},
		{"malformed answer", evs{request(t, "r1")}, answer(t, "a1", alice, "r1", set("channel", nil)), decisionfold.StateOpen},
		{"names another request", evs{request(t, "r1"), request(t, "r2")}, answer(t, "a1", alice, "r2"), decisionfold.StateOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := stable(t, 2, "r1", append(tt.req, tt.answer), nil)
			assert.Equal(t, tt.want, c.State)
		})
	}
}

func TestFold_AnswerWithoutRequestOption_FreeTextNeedsNote(t *testing.T) {
	r := request(t, "r1", freeText)
	noNote := ev(t, "a1", alice, "", 1, ansObj("ka1", "r1", set("option", nil)))
	blank := ev(t, "a2", alice, "", 1, ansObj("ka2", "r1", set("option", nil), set("note", " \t ")))
	good := ev(t, "a3", alice, "", 1, ansObj("ka3", "r1", set("option", nil), set("note", "ship it")))
	assert.Equal(t, decisionfold.StateOpen, card("r1", evs{r, noNote, blank}).State)
	assert.Equal(t, decisionfold.StateAnswered, card("r1", evs{r, noNote, blank, good}).State)
}

func TestFold_StrictAgreeingRule_Table(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob}), set("options", []any{
		obj{"id": "approve"}, obj{"id": "reject"}, obj{"id": "text", "input": obj{"type": "text"}},
	}))
	mk := func(id, by string, mods ...func(obj)) decisionfold.Event { return answer(t, id, by, "r1", mods...) }
	note := func(s string) func(obj) { return set("note", s) }
	inp := func(s string) func(obj) { return set("input_value", s) }
	opt := func(s string) func(obj) { return set("option", s) }
	tests := []struct {
		name string
		a, b decisionfold.Event
		want decisionfold.State
	}{
		{"identical", mk("a1", alice), mk("a2", bob), decisionfold.StateAnswered},
		{"different option", mk("a1", alice), mk("a2", bob, opt("reject")), decisionfold.StateConflicted},
		{"same option different note", mk("a1", alice, note("yes")), mk("a2", bob, note("no")), decisionfold.StateConflicted},
		{"note against no note", mk("a1", alice, note("yes")), mk("a2", bob), decisionfold.StateConflicted},
		{"same option different input", mk("a1", alice, opt("text"), inp("one")), mk("a2", bob, opt("text"), inp("two")), decisionfold.StateConflicted},
		{"same input", mk("a1", alice, opt("text"), inp("one")), mk("a2", bob, opt("text"), inp("one")), decisionfold.StateAnswered},
		{"leading and trailing whitespace agree", mk("a1", alice, note("  yes\n")), mk("a2", bob, note("yes")), decisionfold.StateAnswered},
		{"input whitespace agrees", mk("a1", alice, opt("text"), inp(" x ")), mk("a2", bob, opt("text"), inp("x")), decisionfold.StateAnswered},
		{"case differs", mk("a1", alice, note("Yes")), mk("a2", bob, note("yes")), decisionfold.StateConflicted},
		{"inner whitespace differs", mk("a1", alice, note("a b")), mk("a2", bob, note("a  b")), decisionfold.StateConflicted},
		{"missing note equals empty", mk("a1", alice, note("")), mk("a2", bob), decisionfold.StateAnswered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := stable(t, 3, "r1", evs{r, tt.a, tt.b}, nil)
			assert.Equal(t, tt.want, c.State)
			if tt.want == decisionfold.StateConflicted {
				assert.Equal(t, []string{"a1", "a2"}, c.ConflictingAnswers)
				assert.Empty(t, c.AgreeingAnswers)
			}
		})
	}
}

func TestFold_FreeTextConflict(t *testing.T) {
	r := request(t, "r1", freeText, set("answerers", []any{bob}))
	say := func(id, by, text string) decisionfold.Event {
		return ev(t, id, by, "", 1, ansObj("k"+id, "r1", set("option", nil), set("note", text)))
	}
	assert.Equal(t, decisionfold.StateConflicted, stable(t, 4, "r1", evs{r, say("a1", alice, "yes"), say("a2", bob, "no")}, nil).State)
	assert.Equal(t, decisionfold.StateAnswered, stable(t, 4, "r1", evs{r, say("a1", alice, "yes"), say("a2", bob, " yes ")}, nil).State)
}

func TestFold_MultipleThreeWay_ConflictListsAll(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob, "carol"}))
	c := stable(t, 5, "r1", evs{r, answer(t, "a1", alice, "r1"), answer(t, "a2", bob, "r1", set("option", "reject")),
		answer(t, "a3", "carol", "r1")}, nil)
	assert.Equal(t, decisionfold.StateConflicted, c.State)
	assert.Equal(t, []string{"a1", "a2", "a3"}, c.ConflictingAnswers)
}

func TestFold_Refusal_ReopensAndFlags(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob}))
	a1, a2 := answer(t, "a1", alice, "r1"), answer(t, "a2", bob, "r1", set("option", "reject"))
	t.Run("refusing the only answer reopens", func(t *testing.T) {
		c := stable(t, 6, "r1", evs{r, a1, outcome(t, "o1", "r1", "a1", "refused")}, nil)
		assert.Equal(t, decisionfold.StateOpen, c.State)
		assert.Equal(t, []decisionfold.Flag{decisionfold.FlagReopenedAfterRefusal}, c.Flags)
		assert.Equal(t, []string{"a1"}, c.RefusedAnswers)
	})
	t.Run("refusing one of two conflicting answers leaves the other", func(t *testing.T) {
		c := stable(t, 6, "r1", evs{r, a1, a2, outcome(t, "o1", "r1", "a1", "refused")}, nil)
		assert.Equal(t, decisionfold.StateAnswered, c.State)
		assert.Equal(t, []string{"a2"}, c.AgreeingAnswers)
		assert.Empty(t, c.Flags)
	})
	t.Run("seen_answers refuses several together", func(t *testing.T) {
		o := outcome(t, "o1", "r1", "a1", "refused", set("seen_answers", []any{"a1", "a2"}))
		c := stable(t, 6, "r1", evs{r, a1, a2, o}, nil)
		assert.Equal(t, decisionfold.StateOpen, c.State)
		assert.Equal(t, []string{"a1", "a2"}, c.RefusedAnswers)
		assert.True(t, c.HasFlag(decisionfold.FlagReopenedAfterRefusal))
	})
	t.Run("a new answer after a refusal answers the card", func(t *testing.T) {
		c := stable(t, 6, "r1", evs{r, a1, outcome(t, "o1", "r1", "a1", "refused"), answer(t, "a3", alice, "r1")}, nil)
		assert.Equal(t, decisionfold.StateAnswered, c.State)
		assert.Equal(t, []string{"a3"}, c.AgreeingAnswers)
	})
	t.Run("a refusal by someone else changes nothing", func(t *testing.T) {
		forged := ev(t, "o1", alice, "", 1, outObj("ko1", "r1", "a1", "refused"))
		c := stable(t, 6, "r1", evs{r, a1, forged}, nil)
		assert.Equal(t, decisionfold.StateAnswered, c.State)
		assert.Empty(t, c.Flags)
	})
}

func TestFold_Applied_PinsTheNamedAnswer(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob}))
	a1, a5 := answer(t, "a1", alice, "r1"), answer(t, "a5", bob, "r1")
	t.Run("applied outcome pins", func(t *testing.T) {
		c := stable(t, 7, "r1", evs{r, a5, outcome(t, "o1", "r1", "a5", "applied")}, nil)
		assert.Equal(t, decisionfold.StateApplied, c.State)
		assert.Equal(t, "a5", c.PinnedAnswerID)
		assert.Equal(t, []string{"o1"}, c.OutcomeIDs)
		assert.Empty(t, c.Flags)
	})
	t.Run("a late concurrent answer with a lower id does not unpin", func(t *testing.T) {
		c := stable(t, 7, "r1", evs{r, a5, outcome(t, "o1", "r1", "a5", "applied"), a1}, nil)
		assert.Equal(t, decisionfold.StateApplied, c.State)
		assert.Equal(t, "a5", c.PinnedAnswerID)
		assert.Equal(t, []string{"a1", "a5"}, c.AgreeingAnswers)
	})
	t.Run("a late conflicting answer does not unpin", func(t *testing.T) {
		late := answer(t, "a0", bob, "r1", set("option", "reject"))
		c := stable(t, 7, "r1", evs{r, a5, outcome(t, "o1", "r1", "a5", "applied"), late}, nil)
		assert.Equal(t, decisionfold.StateApplied, c.State)
		assert.Equal(t, "a5", c.PinnedAnswerID)
		assert.Equal(t, []string{"a0"}, c.ConflictingAnswers)
	})
	t.Run("an outcome naming an answer outside V is ignored", func(t *testing.T) {
		c := stable(t, 7, "r1", evs{r, a1, outcome(t, "o1", "r1", "ghost", "applied")}, nil)
		assert.Equal(t, decisionfold.StateAnswered, c.State)
	})
	t.Run("an applied outcome by someone else changes nothing", func(t *testing.T) {
		forged := ev(t, "o1", bob, "", 1, outObj("ko1", "r1", "a1", "applied"))
		assert.Equal(t, decisionfold.StateAnswered, stable(t, 7, "r1", evs{r, a1, forged}, nil).State)
	})
	t.Run("pinned answer is the minimum among those named", func(t *testing.T) {
		o1, o2 := outcome(t, "o1", "r1", "a5", "applied"), outcome(t, "o2", "r1", "a1", "applied")
		c := stable(t, 7, "r1", evs{r, a1, a5, o1, o2}, nil)
		assert.Equal(t, "a1", c.PinnedAnswerID)
		assert.Equal(t, []string{"o1", "o2"}, c.OutcomeIDs)
		assert.Empty(t, c.Flags, "both outcomes name answers with the same value")
	})
}

func TestFold_Applied_OutcomeConflict(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob}))
	a1 := answer(t, "a1", alice, "r1")
	a2 := answer(t, "a2", bob, "r1", set("option", "reject"))
	tests := []struct {
		name   string
		events evs
		pinned string
	}{
		{"applied outcomes name answers of different values", evs{r, a1, a2,
			outcome(t, "o1", "r1", "a2", "applied"), outcome(t, "o2", "r1", "a1", "applied")}, "a1"},
		{"a refusal names the pinned answer", evs{r, a1, outcome(t, "o1", "r1", "a1", "applied"),
			outcome(t, "o2", "r1", "a1", "refused")}, "a1"},
		{"a withdraw by the asker", evs{r, a1, outcome(t, "o1", "r1", "a1", "applied"),
			withdraw(t, "w1", asker, "r1")}, "a1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := stable(t, 8, "r1", tt.events, nil)
			assert.Equal(t, decisionfold.StateApplied, c.State)
			assert.Equal(t, tt.pinned, c.PinnedAnswerID)
			assert.Equal(t, []decisionfold.Flag{decisionfold.FlagOutcomeConflict}, c.Flags)
		})
	}
}

func TestFold_Withdraw_ByAskerOnly(t *testing.T) {
	r := request(t, "r1")
	a1 := answer(t, "a1", alice, "r1")
	t.Run("asker withdraws an open card", func(t *testing.T) {
		c := stable(t, 9, "r1", evs{r, withdraw(t, "w1", asker, "r1")}, nil)
		assert.Equal(t, decisionfold.StateWithdrawn, c.State)
		assert.Equal(t, []string{"w1"}, c.WithdrawIDs)
	})
	t.Run("asker withdraws an answered card", func(t *testing.T) {
		assert.Equal(t, decisionfold.StateWithdrawn, stable(t, 9, "r1", evs{r, a1, withdraw(t, "w1", asker, "r1")}, nil).State)
	})
	t.Run("asker withdraws a conflicted card", func(t *testing.T) {
		r2 := request(t, "r1", set("answerers", []any{bob}))
		b := answer(t, "a2", bob, "r1", set("option", "reject"))
		assert.Equal(t, decisionfold.StateWithdrawn, stable(t, 9, "r1", evs{r2, a1, b, withdraw(t, "w1", asker, "r1")}, nil).State)
	})
	for _, forger := range []string{alice, bob, "cli", "agent-2"} {
		t.Run("forged withdraw by "+forger, func(t *testing.T) {
			c := stable(t, 9, "r1", evs{r, a1, withdraw(t, "w1", forger, "r1")}, nil)
			assert.Equal(t, decisionfold.StateAnswered, c.State)
			assert.Empty(t, c.WithdrawIDs)
			assert.Empty(t, c.Flags)
		})
	}
}

func TestFold_ForgedClocks_ChangeNothing(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob}))
	base := evs{r, answer(t, "a1", alice, "r1"), answer(t, "a2", bob, "r1", set("option", "reject")),
		outcome(t, "o1", "r1", "a1", "refused"), withdraw(t, "w1", alice, "r1")}
	want := card("r1", base)
	for _, lamport := range []int64{0, 1, -5, 1 << 53, 1<<63 - 1} {
		forged := append(evs(nil), base...)
		for i := range forged {
			forged[i].Lamport = lamport
		}
		assert.Equal(t, want, card("r1", forged), "lamport %d", lamport)
	}
	for _, ts := range []int64{0, -1, 1 << 62} {
		forged := append(evs(nil), base...)
		for i := range forged {
			forged[i].WallClockTS = ts
		}
		got := card("r1", forged)
		require.Equal(t, want.State, got.State)
		assert.Equal(t, want.Flags, got.Flags)
		assert.Equal(t, want.AgreeingAnswers, got.AgreeingAnswers)
		assert.Equal(t, want.ConflictingAnswers, got.ConflictingAnswers)
		assert.Equal(t, want.RefusedAnswers, got.RefusedAnswers)
	}
}

func TestFold_TimelineOrder_IsByWallClockThenEventID_AndNeverAffectsState(t *testing.T) {
	r := request(t, "r1")
	a1 := answer(t, "a1", alice, "r1")
	a2 := answer(t, "a2", alice, "r1")
	r.WallClockTS, a1.WallClockTS, a2.WallClockTS = 50, 50, 10
	c := card("r1", evs{a1, r, a2})
	var ids []string
	for _, e := range c.Timeline {
		ids = append(ids, e.EventID)
	}
	assert.Equal(t, []string{"a2", "a1", "r1"}, ids)
	assert.Equal(t, decisionfold.StateAnswered, c.State)
}

func TestFold_Orphans_AreKeptUntilTheRequestArrives(t *testing.T) {
	r := request(t, "r1")
	a1 := answer(t, "a1", alice, "r1")
	w := withdraw(t, "w1", alice, "r1")
	o := outcome(t, "o1", "r1", "a1", "applied")
	missing := card("r1", evs{a1, o, w})
	assert.False(t, missing.Present)
	assert.Equal(t, "r1", missing.RequestID)
	full := stable(t, 10, "r1", evs{a1, o, w, r}, nil)
	assert.True(t, full.Present)
	assert.Equal(t, decisionfold.StateApplied, full.State)
	assert.False(t, card("a1", evs{a1, r}).Present, "an answer is not a request")
}
