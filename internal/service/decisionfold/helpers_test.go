// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"encoding/json"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

type obj = map[string]any

const (
	asker = "agent-1"
	alice = "alice"
	bob   = "bob"
)

func ticket(uid string, ancestors ...string) obj {
	paths := []any{"P-" + uid}
	match := []any{true}
	anc := []any{}
	for _, a := range ancestors {
		anc = append(anc, a)
		paths = append(paths, "P-"+a)
		match = append(match, true)
	}
	return obj{"uid": uid, "ancestors": anc, "paths": paths, "path_match": match}
}

// reqObj is a request with two plain options and one text option.
func reqObj(key string, mods ...func(obj)) obj {
	o := obj{
		"v": 1, "kind": "request", "request_key": key, "question": "Merge?",
		"options": []any{
			obj{"id": "approve"}, obj{"id": "reject"},
			obj{"id": "text", "input": obj{"type": "text", "required": true}},
		},
		"ticket": ticket("t-1"),
	}
	for _, m := range mods {
		m(o)
	}
	return o
}

func set(k string, v any) func(obj) { return func(o obj) { o[k] = v } }

func freeText(o obj) { delete(o, "options") }

func ansObj(key, target string, mods ...func(obj)) obj {
	o := obj{
		"v": 1, "kind": "answer", "request_key": key, "target": target, "option": "approve",
		"decider": obj{"kind": "human"}, "channel": "web",
	}
	for _, m := range mods {
		m(o)
	}
	return o
}

func outObj(key, target, answer, result string, mods ...func(obj)) obj {
	o := obj{"v": 1, "kind": "outcome", "request_key": key, "target": target, "answer": answer, "result": result}
	if result == "refused" {
		o["refusal"] = obj{"text": "stale"}
	}
	for _, m := range mods {
		m(o)
	}
	return o
}

func wdObj(key, target string) obj {
	return obj{"v": 1, "kind": "withdraw", "request_key": key, "target": target}
}

// ev reads a decision object through the real ingest pipeline.
func ev(t testing.TB, id, author, to string, ts int64, d obj) decisionfold.Event {
	t.Helper()
	b, err := json.Marshal(d)
	require.NoError(t, err)
	return evRaw(id, author, to, ts, b)
}

func evRaw(id, author, to string, ts int64, raw []byte) decisionfold.Event {
	ing := model.IngestDecision(raw)
	return decisionfold.Event{
		EventID: id, AuthorID: author, To: to, WallClockTS: ts, Lamport: ts,
		Class: ing.Class, Kind: ing.Kind, Target: ing.Target, RequestKey: ing.RequestKey,
		PayloadSHA256: ing.PayloadSHA256, Decision: ing.Value,
	}
}

func request(t testing.TB, id string, mods ...func(obj)) decisionfold.Event {
	return ev(t, id, asker, alice, 100, reqObj("k-"+id, mods...))
}

func answer(t testing.TB, id, by, target string, mods ...func(obj)) decisionfold.Event {
	return ev(t, id, by, "", 200, ansObj("k-"+id, target, mods...))
}

func outcome(t testing.TB, id, target, ans, result string, mods ...func(obj)) decisionfold.Event {
	return ev(t, id, asker, "", 300, outObj("k-"+id, target, ans, result, mods...))
}

func withdraw(t testing.TB, id, by, target string) decisionfold.Event {
	return ev(t, id, by, "", 400, wdObj("k-"+id, target))
}

func card(id string, events []decisionfold.Event, deletes ...decisionfold.Delete) decisionfold.Card {
	return decisionfold.Fold(id, events, deletes)
}

// permutations calls fn with n seeded shuffles of events. The seed is part
// of every failure message through the label it passes.
func permutations(seed uint64, n int, events []decisionfold.Event, fn func(label string, perm []decisionfold.Event)) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for i := range n {
		perm := append([]decisionfold.Event(nil), events...)
		rng.Shuffle(len(perm), func(a, b int) { perm[a], perm[b] = perm[b], perm[a] })
		fn("seed "+itoa(int(seed))+" permutation "+itoa(i), perm)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// stable checks that every shuffle of events gives the same card for id.
func stable(t *testing.T, seed uint64, id string, events []decisionfold.Event, deletes []decisionfold.Delete) decisionfold.Card {
	t.Helper()
	want := decisionfold.Fold(id, events, deletes)
	permutations(seed, 60, events, func(label string, perm []decisionfold.Event) {
		require.Equal(t, want, decisionfold.Fold(id, perm, deletes), label)
	})
	return want
}
