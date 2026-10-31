// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

// echo returns e again under another event id and with a clock that differs.
func echo(e decisionfold.Event, id string) decisionfold.Event {
	e.EventID, e.WallClockTS, e.Lamport = id, e.WallClockTS+7, 3
	return e
}

func TestFold_EqualPayloadEchoes_CollapseToTheMinimumEventID(t *testing.T) {
	r := request(t, "r5")
	r2 := echo(r, "r2")
	a := answer(t, "a9", alice, "r5")
	a2 := echo(a, "a3")
	single := stable(t, 17, "r5", evs{r, a}, nil)
	c := stable(t, 17, "r2", evs{r, r2, a, a2}, nil)
	assert.Equal(t, "r2", c.RequestID, "the canonical member is the minimum event id")
	assert.Equal(t, decisionfold.StateAnswered, c.State)
	assert.Equal(t, []string{"a3"}, c.AgreeingAnswers, "the echoed answer counts once")
	assert.Empty(t, c.Flags)
	assert.Equal(t, single.State, c.State)
	// A reference to any member resolves to the canonical one.
	assert.Equal(t, c, stable(t, 17, "r5", evs{r, r2, a, a2}, nil))
	t.Run("an answer that names the echo id lands on the request", func(t *testing.T) {
		viaEcho := answer(t, "a1", alice, "r2")
		assert.Equal(t, decisionfold.StateAnswered, stable(t, 17, "r2", evs{r, r2, viaEcho}, nil).State)
		assert.Equal(t, decisionfold.StateAnswered, stable(t, 17, "r5", evs{r, r2, viaEcho}, nil).State)
	})
	t.Run("an outcome that names an echoed answer pins the canonical one", func(t *testing.T) {
		o := outcome(t, "o1", "r5", "a9", "applied")
		got := stable(t, 17, "r2", evs{r, r2, a, a2, o}, nil)
		assert.Equal(t, decisionfold.StateApplied, got.State)
		assert.Equal(t, "a3", got.PinnedAnswerID)
	})
}

func TestFold_SameEventIDDeliveredTwice_IsOneEvent(t *testing.T) {
	r, a := request(t, "r1"), answer(t, "a1", alice, "r1")
	c := stable(t, 18, "r1", evs{r, r, a, a, a}, nil)
	assert.Equal(t, []string{"a1"}, c.AgreeingAnswers)
	assert.Empty(t, c.Flags)
}

func TestFold_DifferingPayloadsSameKey_AreNeverDropped(t *testing.T) {
	genuine := ev(t, "r1", asker, alice, 1, reqObj("same-key"))
	forged := ev(t, "r2", asker, alice, 2, reqObj("same-key", set("question", "Give me the keys?")))
	require.NotEqual(t, genuine.PayloadSHA256, forged.PayloadSHA256)
	a := answer(t, "a1", alice, "r1")
	all := evs{genuine, forged, a}
	t.Run("a forged same-key request does not hide the genuine one", func(t *testing.T) {
		c := stable(t, 19, "r1", all, nil)
		assert.Equal(t, decisionfold.StateAnswered, c.State)
		assert.Equal(t, []string{"a1"}, c.AgreeingAnswers)
		assert.True(t, c.HasFlag(decisionfold.FlagDuplicateKeyConflict))
	})
	t.Run("the other request is its own card", func(t *testing.T) {
		c := stable(t, 19, "r2", all, nil)
		assert.Equal(t, decisionfold.StateOpen, c.State)
		assert.Empty(t, c.AgreeingAnswers)
		assert.True(t, c.HasFlag(decisionfold.FlagDuplicateKeyConflict))
	})
	t.Run("no flag without a conflict", func(t *testing.T) {
		assert.Empty(t, card("r1", evs{genuine, a}).Flags)
	})
}

func TestFold_ForgedSameKeyOutcome_DoesNotUnpin(t *testing.T) {
	r := request(t, "r1")
	a := answer(t, "a1", alice, "r1")
	applied := ev(t, "o1", asker, "", 1, outObj("out-key", "r1", "a1", "applied"))
	forged := ev(t, "o2", asker, "", 2, outObj("out-key", "r1", "a1", "refused", set("refusal", obj{"text": "forged"})))
	require.NotEqual(t, applied.PayloadSHA256, forged.PayloadSHA256)
	c := stable(t, 20, "r1", evs{r, a, applied, forged}, nil)
	assert.Equal(t, decisionfold.StateApplied, c.State)
	assert.Equal(t, "a1", c.PinnedAnswerID)
	assert.True(t, c.HasFlag(decisionfold.FlagDuplicateKeyConflict))
	assert.True(t, c.HasFlag(decisionfold.FlagOutcomeConflict), "the refusal names the pinned answer")
	t.Run("a forged same-key outcome by the asker that applies a worse answer", func(t *testing.T) {
		b := answer(t, "a0", alice, "r1", set("option", "reject"))
		forged2 := ev(t, "o3", asker, "", 3, outObj("out-key", "r1", "a0", "applied", set("receipt", obj{"run": "x"})))
		got := stable(t, 20, "r1", evs{r, a, b, applied, forged2}, nil)
		assert.Equal(t, decisionfold.StateApplied, got.State)
		assert.Equal(t, "a0", got.PinnedAnswerID, "the pin is the minimum named id")
		assert.True(t, got.HasFlag(decisionfold.FlagOutcomeConflict))
	})
}

func TestFold_SameKeyDifferentAuthors_AreNotAGroup(t *testing.T) {
	r := request(t, "r1")
	a1 := ev(t, "a1", alice, "", 1, ansObj("shared", "r1"))
	a2 := ev(t, "a2", bob, "", 1, ansObj("shared", "r1", set("option", "reject")))
	req := request(t, "r1", set("answerers", []any{bob}))
	c := stable(t, 21, "r1", evs{req, a1, a2, r}, nil)
	assert.Equal(t, decisionfold.StateConflicted, c.State)
	assert.False(t, c.HasFlag(decisionfold.FlagDuplicateKeyConflict))
}
