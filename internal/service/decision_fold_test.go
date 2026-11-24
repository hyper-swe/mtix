// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Fixtures represent the indexer's decoded journal facts. Distinct hashes stand
// for distinct payloads; an echo copies the entire payload and changes only ID.
func foldEvent(id, author, kind string) DecisionFoldEvent {
	p := model.DecisionPayload{DecisionHeader: model.DecisionHeader{Kind: kind, RequestKey: id, V: 1}, Target: "r"}
	return DecisionFoldEvent{ID: id, Author: author, To: "user-a", DecodedDecision: model.DecodedDecision{Payload: p, PayloadSHA256: id}}
}
func foldRequest(id string) DecisionFoldEvent {
	e := foldEvent(id, "asker", "request")
	e.Payload.Target = ""
	e.Payload.DecisionRequest = &model.DecisionRequest{AnswerableBy: "user", Answerers: []string{"delegate"}, RequiresOutcome: true,
		Ticket: model.DecisionTicket{UID: "uid", Paths: []string{"T-1"}, PathMatch: []bool{true}}}
	return e
}
func foldAnswer(id, note string) DecisionFoldEvent {
	e := foldEvent(id, "user-a", "answer")
	e.Payload.DecisionAnswer = &model.DecisionAnswer{Note: note, Decider: model.DecisionActor{Kind: "user"}}
	return e
}
func foldOutcome(id, result, answer string, seen ...string) DecisionFoldEvent {
	e := foldEvent(id, "asker", "outcome")
	e.Payload.DecisionOutcome = &model.DecisionOutcome{Result: result, Answer: answer, SeenAnswers: seen}
	return e
}
func card(events ...DecisionFoldEvent) DecisionFoldCard { return FoldDecisions(events, nil)["r"] }

func TestFoldDecisions_AgreeingRequiresEntireNormalizedTriple(t *testing.T) {
	for _, tc := range []struct{ name, option, input, note, state string }{
		{"agree", "change", " café ", " Réviser ", "answered"},
		{"input case", "change", "Café", "Réviser", "conflicted"},
		{"different note", "change", "café", "réviser", "conflicted"},
		{"inner whitespace", "change", "ca fé", "Réviser", "conflicted"},
		{"different option", "other", "café", "Réviser", "conflicted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := foldRequest("r")
			for _, id := range []string{"change", "other"} {
				r.Payload.Options = append(r.Payload.Options, model.DecisionOption{ID: id, Input: &model.DecisionInput{Type: "text", Required: true}, NoteRequired: true})
			}
			a, b := foldAnswer("b", "Re\u0301viser"), foldAnswer("a", tc.note)
			a.Payload.Option, a.Payload.InputValue = "change", "cafe\u0301"
			b.Author, b.Payload.Option, b.Payload.InputValue = "delegate", tc.option, tc.input
			got := card(r, a, b)
			require.EqualValues(t, tc.state, got.State)
			if tc.state == "answered" {
				require.Equal(t, []string{"a", "b"}, got.AgreeingAnswers)
				require.Equal(t, "a", got.EffectiveAnswer)
			}
		})
	}
}

func TestFoldDecisions_OnlyValidAnswersCanPin(t *testing.T) {
	for _, tc := range []struct{ author, kind, allowed, option, input, note, state string }{
		{"user-a", "user", "user", "x", "F-1", "ok", "applied"},
		{"delegate", "agent", "any", "x", "F-1", "ok", "applied"},
		{"delegate", "agent", "user", "x", "F-1", "ok", "open"},
		{"cli", "user", "any", "x", "F-1", "ok", "open"},
		{"asker", "user", "any", "x", "F-1", "ok", "open"},
		{"other", "user", "any", "x", "F-1", "ok", "open"},
		{"user-a", "user", "user", "y", "F-1", "ok", "open"},
		{"user-a", "user", "user", "x", " ", "ok", "open"},
		{"user-a", "user", "user", "x", "bad value", "ok", "open"},
		{"user-a", "user", "user", "x", "F-1", " ", "open"},
	} {
		r, a := foldRequest("r"), foldAnswer("a", tc.note)
		r.Payload.Answerers = append(r.Payload.Answerers, "cli")
		if tc.author == "asker" {
			r.To = "asker"
		}
		r.Payload.AnswerableBy = tc.allowed
		r.Payload.Options = []model.DecisionOption{{ID: "x", NoteRequired: true, Input: &model.DecisionInput{Type: "finding_id", Required: true}}}
		a.Author, a.Payload.Decider.Kind, a.Payload.Option, a.Payload.InputValue = tc.author, tc.kind, tc.option, tc.input
		require.EqualValues(t, tc.state, card(r, a, foldOutcome("o", "applied", "a")).State, "%+v", tc)
	}
}

func TestFoldDecisions_MergedAnswerValidationControlsPinning(t *testing.T) {
	r := foldRequest("r")
	r.Payload.Links = []model.DecisionLink{{OpenBeforeAnswer: true}}
	r.Payload.Options = []model.DecisionOption{{ID: "go", Input: &model.DecisionInput{Type: "text"}}}
	longRun := "a" + strings.Repeat("\u0301", 31)
	for _, tc := range []struct {
		name, input, note string
		opened            []int
		want              DecisionState
	}{
		{"valid answer", "ok", "yes", []int{0}, DecisionApplied},
		{"required link absent", "ok", "yes", nil, DecisionOpen},
		{"input cannot be normalized", longRun, "yes", []int{0}, DecisionOpen},
		{"note cannot be normalized", "ok", longRun, []int{0}, DecisionOpen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := foldAnswer("a", tc.note)
			a.Payload.Option, a.Payload.InputValue, a.Payload.OpenedLinks = "go", tc.input, tc.opened
			require.Equal(t, tc.want, card(r, a, foldOutcome("o", "applied", "a")).State)
		})
	}
}

func TestFoldDecisions_AuthorOutcomesPinRefuseAndWithdraw(t *testing.T) {
	r, a, b := foldRequest("r"), foldAnswer("a", "yes"), foldAnswer("b", "no")
	apply, refuse := foldOutcome("apply", "applied", "b"), foldOutcome("no", "refused", "b", "a", "b")
	w := foldEvent("w", "asker", "withdraw")
	w.Payload.DecisionWithdraw = &model.DecisionWithdraw{ReasonCode: "answered_elsewhere"}
	require.EqualValues(t, "conflicted", card(r, a, b).State)
	got := card(r, b, apply, a)
	require.EqualValues(t, "applied", got.State)
	require.Equal(t, "b", got.PinnedAnswerID)
	require.False(t, got.OutcomeConflict)
	require.True(t, card(r, a, b, apply, w).OutcomeConflict)
	require.True(t, card(r, a, b, apply, refuse).OutcomeConflict)
	got = card(r, a, b, apply, foldOutcome("second", "applied", "a"))
	require.True(t, got.OutcomeConflict)
	require.Equal(t, "a", got.PinnedAnswerID)
	got = card(r, a, b, refuse)
	require.True(t, got.ReopenedAfterRefusal)
	require.EqualValues(t, "open", got.State)
	require.EqualValues(t, "withdrawn", card(r, a, w).State)
	w.Author, apply.Author, refuse.Author = "other", "other", "other"
	require.EqualValues(t, "answered", card(r, a, w, apply, refuse).State)
	r.Payload.RequiresOutcome = false
	require.EqualValues(t, "conflicted", card(r, a, b, foldOutcome("o", "applied", "b")).State)
}

func TestFoldDecisions_EchoReferencesConflictingKeysAndPermutations(t *testing.T) {
	r, a := foldRequest("r"), foldAnswer("a", "yes")
	re, ae := r, a
	re.ID, ae.ID = "rr", "aa"
	a.Payload.Target, ae.Payload.Target = "rr", "rr"
	o := foldOutcome("out", "applied", "aa")
	forged := foldOutcome("forged", "refused", "aa", "aa")
	forged.Payload.RequestKey = o.Payload.RequestKey
	other := foldRequest("other")
	other.Payload.RequestKey = r.Payload.RequestKey
	events := []DecisionFoldEvent{re, ae, a, o, r, forged, other}
	want := FoldDecisions(events, nil)
	require.Len(t, want, 2)
	require.True(t, want["other"].DuplicateKeyConflict)
	got := want["r"]
	require.True(t, got.DuplicateKeyConflict)
	require.True(t, got.OutcomeConflict)
	require.Equal(t, "a", got.PinnedAnswerID)
	require.EqualValues(t, "applied", got.State)
	// Visit every permutation, including replies before their requests.
	var visit func(int)
	visit = func(i int) {
		if i == len(events) {
			require.Equal(t, want, FoldDecisions(events, nil))
			return
		}
		for j := i; j < len(events); j++ {
			events[i], events[j] = events[j], events[i]
			visit(i + 1)
			events[i], events[j] = events[j], events[i]
		}
	}
	visit(0)
	require.Empty(t, FoldDecisions([]DecisionFoldEvent{a, o, ae}, nil))
	require.EqualValues(t, "open", card(r, a, o).State) // absent alias targets
}

func TestFoldDecisions_InvalidAndOpaqueEvents(t *testing.T) {
	r, a := foldRequest("r"), foldAnswer("a", "ok")
	o := foldOutcome("o", "applied", "a")
	for _, e := range []DecisionFoldEvent{r, a, o} {
		for _, reason := range []string{"malformed", "unsupported", "redacted"} {
			bad := e
			switch reason {
			case "malformed":
				bad.DecodeErr = model.ErrInvalidInput
			case "unsupported":
				bad.DecodeErr = model.ErrDecisionUnsupported
			case "redacted":
				bad.Redacted = true
			}
			events := []DecisionFoldEvent{r, a, o}
			events[slices.IndexFunc(events, func(x DecisionFoldEvent) bool { return x.ID == bad.ID })] = bad
			got := card(events...)
			switch bad.ID {
			case "r":
				require.EqualValues(t, "invalid", got.State)
				require.Equal(t, reason, got.InvalidReason)
			case "a":
				require.EqualValues(t, "open", got.State)
			case "o":
				require.EqualValues(t, "answered", got.State)
			}
			require.Equal(t, reason == "redacted", got.Redacted)
			if bad.ID != "r" {
				require.Equal(t, reason == "unsupported", got.UnsupportedEvents)
			}
		}
	}
}

// Own, relay and hub copies can have different clocks, delivery metadata and
// host configuration. Only the immutable comment fields cross the fold boundary.
func TestFoldDecisions_StampedAndUnstampedReplicasConverge(t *testing.T) {
	events := []DecisionFoldEvent{foldRequest("r"), foldAnswer("a", "yes"), foldOutcome("o", "applied", "a"), foldAnswer("b", "no")}
	want := FoldDecisions(events, nil)
	for _, local := range []string{"founder", "other", "cli"} {
		t.Setenv("MTIX_AUTHOR_ID", local)
		copies := make([]DecisionFoldEvent, 0, len(events))
		for i, e := range events {
			journal := model.SyncEvent{EventID: e.ID, AuthorID: e.Author, WallClockTS: int64(i + 1), LamportClock: int64(i + 2), SyncStatus: model.SyncStatusPushed}
			if local != "founder" {
				journal.LamportClock = 0
				journal.WallClockTS = 0
				journal.SyncStatus = model.SyncStatusPending
			}
			// A future principal stamp, like the protocol-1 transport metadata,
			// is outside the decoded decision and can be stripped on relay.
			stamp := ""
			if local == "founder" {
				stamp = "hub-user"
			}
			delivery := struct {
				model.SyncEvent
				PrincipalStamp string
			}{journal, stamp}
			copies = append(copies, NewDecisionFoldEvent(delivery.SyncEvent, e.To, e.DecodedDecision, e.DecodeErr))
		}
		require.Equal(t, want, FoldDecisions(copies, nil))
	}
}

func TestFoldDecisions_RefusalSubtractsOnlyNamedAnswersOnThisCard(t *testing.T) {
	r, a, b, c := foldRequest("r"), foldAnswer("a", "yes"), foldAnswer("b", " yes "), foldAnswer("c", "no")
	o := foldOutcome("o", "refused", "c", "c")
	got := card(r, a, b, c, o)
	require.EqualValues(t, "answered", got.State)
	require.Equal(t, []string{"a", "b"}, got.AgreeingAnswers)
	require.False(t, got.ReopenedAfterRefusal)
	first, second := foldOutcome("first", "applied", "a"), foldOutcome("second", "applied", "b")
	got = card(r, a, b, c, o, second, first)
	require.Equal(t, "a", got.PinnedAnswerID)
	require.False(t, got.OutcomeConflict)
	first.Author = "forger"
	o.Author = "forger"
	require.EqualValues(t, "conflicted", card(r, a, b, c, first, o).State)
	b.Payload.Target = "other"
	require.EqualValues(t, "open", card(r, b, second).State)
}

func TestFoldDecisions_HigherPriorityStatesSurviveSupersession(t *testing.T) {
	r, next := foldRequest("r"), foldRequest("next")
	next.Payload.Supersedes = []string{"r"}
	a, b := foldAnswer("a", "yes"), foldAnswer("b", "no")
	w := foldEvent("w", "asker", "withdraw")
	w.Payload.DecisionWithdraw = &model.DecisionWithdraw{}
	require.EqualValues(t, "superseded", card(r, next, a, b).State)
	require.EqualValues(t, "withdrawn", card(r, next, a, b, w).State)
	require.EqualValues(t, "applied", card(r, next, a, b, w, foldOutcome("o", "applied", "a")).State)
}

func TestFoldDecisions_DuplicateConflictFollowsEachTarget(t *testing.T) {
	r, next := foldRequest("r"), foldRequest("next")
	a, b := foldAnswer("a", "yes"), foldAnswer("b", "no")
	b.Payload.RequestKey = a.Payload.RequestKey
	b.Payload.Target = "next"
	for _, author := range []string{"user-a", "delegate"} {
		b.Author = author
		got := FoldDecisions([]DecisionFoldEvent{r, next, a, b}, nil)
		for _, id := range []string{"r", "next"} {
			require.EqualValues(t, "answered", got[id].State)
			require.Equal(t, author == "user-a", got[id].DuplicateKeyConflict)
		}
	}
}

func TestFoldDecisions_DecodedWireDefaultsAndFutureReplies(t *testing.T) {
	const id = "00000000-0000-7000-8000-000000000001"
	type wire struct{ id, author, raw string }
	wires := []wire{
		{id, "asker", `{"v":1,"kind":"request","request_key":"r","question":"Ship?","ticket":{"uid":"u","ancestors":[],"paths":["TEST-1"],"path_match":[true],"truncated":false}}`},
		{"00000000-0000-7000-8000-000000000002", "user-a", `{"v":1,"kind":"answer","target":"` + id + `","request_key":"a","note":"Yes","decider":{"kind":"user"},"channel":"web"}`},
		{"00000000-0000-7000-8000-000000000003", "asker", `{"v":2,"kind":"withdraw","target":"` + id + `","request_key":"w"}`},
	}
	var events []DecisionFoldEvent
	for i, w := range wires {
		d, err := model.DecodeDecision([]byte(w.raw), w.author, false)
		if i == 2 {
			require.ErrorIs(t, err, model.ErrDecisionUnsupported)
		} else {
			require.NoError(t, err)
		}
		events = append(events, NewDecisionFoldEvent(model.SyncEvent{EventID: w.id, AuthorID: w.author}, "user-a", d, err))
	}
	got := FoldDecisions(events, nil)[id]
	require.EqualValues(t, "answered", got.State)
	require.True(t, got.UnsupportedEvents)
	require.Equal(t, wires[1].id, got.EffectiveAnswer)
}

func TestFoldDecisions_OpaqueRequestsCannotConsumeEachOther(t *testing.T) {
	a, b := foldEvent("a", "asker", "request"), foldEvent("b", "asker", "request")
	for _, e := range []*DecisionFoldEvent{&a, &b} {
		e.Payload.RequestKey = ""
		e.Payload.Target = ""
		e.PayloadSHA256 = ""
		e.DecodeErr = model.ErrInvalidInput
	}
	got := FoldDecisions([]DecisionFoldEvent{a, b}, nil)
	require.Len(t, got, 2)
	for _, c := range got {
		require.EqualValues(t, "invalid", c.State)
		require.Equal(t, "malformed", c.InvalidReason)
		require.False(t, c.DuplicateKeyConflict)
	}
}
