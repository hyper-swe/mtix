// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

func TestFold_InvalidRequests_ReasonAndPriority(t *testing.T) {
	redacted := func(e decisionfold.Event) decisionfold.Event { e.Redacted = true; return e }
	del := []decisionfold.Delete{{UID: "t-1"}}
	tests := []struct {
		name    string
		req     decisionfold.Event
		deletes []decisionfold.Delete
		reason  decisionfold.InvalidReason
		flags   []decisionfold.Flag
	}{
		{"malformed", request(t, "r1", set("question", "")), nil, decisionfold.ReasonMalformed, nil},
		{"not even JSON", evRaw("r1", asker, alice, 1, []byte(`{"v":`)), nil, decisionfold.ReasonMalformed, nil},
		{"unsupported version", request(t, "r1", set("v", 2)), nil, decisionfold.ReasonUnsupported, nil},
		{"unsupported enum", request(t, "r1", set("answerable_by", "robot")), nil, decisionfold.ReasonUnsupported, nil},
		{"redacted", redacted(request(t, "r1")), nil, decisionfold.ReasonRedacted, []decisionfold.Flag{decisionfold.FlagRedacted}},
		{"ticket deleted", request(t, "r1"), del, decisionfold.ReasonTicketDeleted,
			[]decisionfold.Flag{decisionfold.FlagTicketDeleted}},
		{"malformed beats redacted", redacted(request(t, "r1", set("question", ""))), nil, decisionfold.ReasonMalformed,
			[]decisionfold.Flag{decisionfold.FlagRedacted}},
		{"redacted beats ticket deleted", redacted(request(t, "r1")), del, decisionfold.ReasonRedacted,
			[]decisionfold.Flag{decisionfold.FlagTicketDeleted, decisionfold.FlagRedacted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := stable(t, 15, "r1", evs{tt.req, answer(t, "a1", alice, "r1")}, tt.deletes)
			assert.Equal(t, decisionfold.StateInvalid, c.State)
			assert.Equal(t, tt.reason, c.InvalidReason)
			assert.Equal(t, tt.flags, c.Flags)
			assert.Empty(t, c.AgreeingAnswers)
			assert.NotEmpty(t, c.Timeline)
		})
	}
}

func TestFold_InvalidBeatsEverything(t *testing.T) {
	r := request(t, "r1")
	all := evs{r, answer(t, "a1", alice, "r1"), outcome(t, "o1", "r1", "a1", "applied"), withdraw(t, "w1", asker, "r1")}
	assert.Equal(t, decisionfold.StateApplied, card("r1", all).State)
	c := card("r1", all, decisionfold.Delete{UID: "t-1"})
	assert.Equal(t, decisionfold.StateInvalid, c.State)
	assert.Equal(t, decisionfold.ReasonTicketDeleted, c.InvalidReason)
}

func TestFold_UnsupportedAndMalformedEvents_AreExcludedAndFlagged(t *testing.T) {
	r := request(t, "r1")
	good := answer(t, "a1", alice, "r1")
	tests := []struct {
		name    string
		extra   decisionfold.Event
		flagged bool
	}{
		{"future version answer", answer(t, "a2", alice, "r1", set("v", 2)), true},
		{"unknown decider kind", answer(t, "a2", alice, "r1", set("decider", obj{"kind": "daemon"})), true},
		{"unknown outcome result", outcome(t, "o1", "r1", "a1", "deferred"), true},
		{"unknown kind", ev(t, "x1", asker, "", 1, obj{"v": 1, "kind": "vote", "request_key": "kx", "target": "r1"}), true},
		{"future version withdraw", ev(t, "w1", asker, "", 1, obj{"v": 2, "kind": "withdraw", "request_key": "kw", "target": "r1"}), true},
		{"malformed answer", answer(t, "a2", alice, "r1", set("channel", 5)), false},
		{"malformed withdraw", ev(t, "w1", asker, "", 1, obj{"v": 1, "kind": "withdraw", "request_key": "kw", "target": "r1", "reason": 5}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := stable(t, 16, "r1", evs{r, good, tt.extra}, nil)
			assert.Equal(t, decisionfold.StateAnswered, c.State, "the excluded event changes no state")
			assert.Equal(t, []string{"a1"}, c.AgreeingAnswers)
			assert.Equal(t, tt.flagged, c.HasFlag(decisionfold.FlagUnsupportedEvents))
			var found, counted bool
			for _, e := range c.Timeline {
				if e.EventID == tt.extra.EventID {
					found, counted = true, e.Counted
				}
			}
			assert.True(t, found, "the event is kept and shown")
			assert.False(t, counted)
		})
	}
}

func TestFold_AnswerToAnotherRequest_IsNotShownHere(t *testing.T) {
	c := card("r1", evs{request(t, "r1"), request(t, "r2"), answer(t, "a1", alice, "r2")})
	assert.Len(t, c.Timeline, 1)
}
