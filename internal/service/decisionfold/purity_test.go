// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package decisionfold_test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service/decisionfold"
)

// The fold's inputs name no hub stamp, ack state, arrival order or node row.
func TestFold_InputTypes_CarryOnlyAllowedFields(t *testing.T) {
	names := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, rt.Field(i).Name)
		}
		return out
	}
	assert.ElementsMatch(t, []string{"EventID", "AuthorID", "To", "WallClockTS", "Lamport", "Class", "Kind", "Target",
		"RequestKey", "PayloadSHA256", "Decision", "Redacted"}, names(decisionfold.Event{}))
	assert.ElementsMatch(t, []string{"UID", "NodeID", "Cascade", "Path"}, names(decisionfold.Delete{}))
	rt := reflect.TypeOf(decisionfold.Fold)
	require.Equal(t, 3, rt.NumIn(), "Fold takes the request id, the events and the deletes, nothing else")
}

// The package imports nothing that could read configuration, a clock,
// randomness, the file system or local node rows.
func TestFold_Sources_ImportOnlyPureDependencies(t *testing.T) {
	allowed := map[string]bool{
		"cmp": true, "slices": true, "encoding/json": true,
		"github.com/hyper-swe/mtix/internal/model": true,
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		checked++
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range af.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			assert.True(t, allowed[path], "%s imports %s", f, path)
		}
		src, err := os.ReadFile(f) //nolint:gosec // reads this package's own sources
		require.NoError(t, err)
		for _, banned := range []string{"time.Now", "os.Getenv", "rand.", "global", "init()"} {
			assert.NotContains(t, string(src), banned, f)
		}
	}
	assert.GreaterOrEqual(t, checked, 7)
}

func syncComment(t *testing.T, e decisionfold.Event, d obj) model.SyncEvent {
	t.Helper()
	payload, err := json.Marshal(obj{"author_id": e.AuthorID, "to": e.To, "body": "DECISION NEEDED", "decision": d})
	require.NoError(t, err)
	now := time.Now()
	return model.SyncEvent{
		EventID: e.EventID, ProjectPrefix: "P", NodeID: "P-1", OpType: model.OpComment, Payload: payload,
		WallClockTS: e.WallClockTS, LamportClock: e.Lamport,
		AuthorID: "hub-principal-9", AuthorMachineHash: "0123456789abcdef", VectorClock: model.VectorClock{"m": 99},
		SyncStatus: model.SyncStatus("pushed"), CreatedAt: now, RetainedUntil: &now,
	}
}

func stripStamps(se model.SyncEvent) model.SyncEvent {
	se.AuthorID, se.AuthorMachineHash, se.VectorClock = "", "", model.VectorClock{}
	se.SyncStatus, se.CreatedAt, se.RetainedUntil, se.Malformed = "", time.Time{}, nil, ""
	return se
}

func TestFold_StripEveryStampAndChangeConfig_SameCard(t *testing.T) {
	r := request(t, "r1", set("answerers", []any{bob}))
	parts := []struct {
		e decisionfold.Event
		d obj
	}{
		{r, reqObj("k-r1", set("answerers", []any{bob}))},
		{answer(t, "a1", alice, "r1"), ansObj("k-a1", "r1")},
		{answer(t, "a2", bob, "r1", set("option", "reject")), ansObj("k-a2", "r1", set("option", "reject"))},
		{outcome(t, "o1", "r1", "a1", "refused"), outObj("k-o1", "r1", "a1", "refused")},
	}
	build := func(strip bool) evs {
		var out evs
		for _, p := range parts {
			se := syncComment(t, p.e, p.d)
			if strip {
				se = stripStamps(se)
			}
			ev, ok := decisionfold.EventFromSync(se)
			require.True(t, ok)
			out = append(out, ev)
		}
		return out
	}
	want := card("r1", build(false))
	assert.Equal(t, decisionfold.StateAnswered, want.State)
	assert.Equal(t, want, card("r1", build(true)))
	for name, env := range map[string]map[string]string{
		"time zone":  {"TZ": "Pacific/Kiritimati"},
		"mtix keys":  {"MTIX_DECISIONS_ENABLED": "true", "MTIX_WAKE_MAX_HOP_DEPTH": "1"},
		"no config":  {"MTIX_CONFIG": "/nonexistent", "HOME": t.TempDir()},
		"author env": {"MTIX_AUTHOR_ID": asker},
	} {
		for k, v := range env {
			t.Setenv(k, v)
		}
		assert.Equal(t, want, card("r1", build(true)), name)
	}
}

func TestEventFromSync_Table(t *testing.T) {
	r := request(t, "r1")
	se := syncComment(t, r, reqObj("k-r1"))
	ev, ok := decisionfold.EventFromSync(se)
	require.True(t, ok)
	assert.Equal(t, decisionfold.Event{EventID: "r1", AuthorID: asker, To: alice, WallClockTS: 100, Lamport: 100,
		Class: model.DecisionValid, Kind: model.DecisionKindRequest, RequestKey: "k-r1", PayloadSHA256: r.PayloadSHA256,
		Decision: r.Decision}, ev)

	notComment := se
	notComment.OpType = model.OpUpdateField
	_, ok = decisionfold.EventFromSync(notComment)
	assert.False(t, ok)

	plain := se
	plain.Payload = []byte(`{"author_id":"a","body":"hi"}`)
	_, ok = decisionfold.EventFromSync(plain)
	assert.False(t, ok)

	badPayload := se
	badPayload.Payload = []byte(`{"author_id":5,"decision":{"v":1}}`)
	_, ok = decisionfold.EventFromSync(badPayload)
	assert.False(t, ok)

	malformed := se
	malformed.Payload = []byte(`{"author_id":"a","decision":{"v":1,"kind":"answer","target":"r1","request_key":"k"}}`)
	ev, ok = decisionfold.EventFromSync(malformed)
	require.True(t, ok)
	assert.Equal(t, model.DecisionMalformed, ev.Class)
	assert.Equal(t, "r1", ev.Target)
}

func TestDeleteFromSync_Table(t *testing.T) {
	base := model.SyncEvent{OpType: model.OpDelete, UID: "u1", NodeID: "P-1"}
	tests := []struct {
		name    string
		payload string
		want    decisionfold.Delete
	}{
		{"current binaries write neither field", `null`, decisionfold.Delete{UID: "u1", NodeID: "P-1"}},
		{"empty object", `{}`, decisionfold.Delete{UID: "u1", NodeID: "P-1"}},
		{"cascade and path", `{"cascade":true,"path":"P-1.2"}`, decisionfold.Delete{UID: "u1", NodeID: "P-1", Cascade: true, Path: "P-1.2"}},
		{"wrong types read as absent", `{"cascade":"yes","path":5}`, decisionfold.Delete{UID: "u1", NodeID: "P-1"}},
		{"not JSON", `nope`, decisionfold.Delete{UID: "u1", NodeID: "P-1"}},
		{"unknown fields ignored", `{"extra":1}`, decisionfold.Delete{UID: "u1", NodeID: "P-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := base
			se.Payload = []byte(tt.payload)
			d, ok := decisionfold.DeleteFromSync(se)
			require.True(t, ok)
			assert.Equal(t, tt.want, d)
		})
	}
}

// soup builds a random but valid-looking event set over a small id space.
func soup(t *testing.T, rng *rand.Rand) (evs, []decisionfold.Delete) {
	t.Helper()
	pick := func(xs ...string) string { return xs[rng.IntN(len(xs))] }
	var events evs
	for i := range 1 + rng.IntN(4) {
		id := "r" + strconv.Itoa(i+1)
		mods := []func(obj){set("answerers", []any{bob})}
		if rng.IntN(2) == 0 {
			mods = append(mods, keyed(pick("ka", "kb")))
		}
		if rng.IntN(3) == 0 {
			mods = append(mods, superseding("r"+strconv.Itoa(1+rng.IntN(4))))
		}
		if rng.IntN(5) == 0 {
			mods = append(mods, set("ticket", ticket(pick("t-1", "t-2"), "t-p")))
		}
		author := pick(asker, asker, "agent-2")
		events = append(events, ev(t, id, author, alice, int64(rng.IntN(50)), reqObj("k-"+id, mods...)))
		if rng.IntN(4) == 0 {
			events = append(events, echo(events[len(events)-1], id+"x"))
		}
	}
	for i := range rng.IntN(10) {
		target := "r" + strconv.Itoa(1+rng.IntN(4))
		id := "e" + strconv.Itoa(i)
		by := pick(alice, bob, asker, "agent-2", "cli")
		var d obj
		switch rng.IntN(4) {
		case 0:
			d = wdObj("k-"+id, target)
		case 1:
			d = outObj("k-"+id, target, "e"+strconv.Itoa(rng.IntN(10)), pick("applied", "refused"))
		default:
			d = ansObj("k-"+id, target, set("option", pick("approve", "reject")), set("note", pick("", "n", " n ")))
		}
		events = append(events, ev(t, id, by, "", int64(rng.IntN(50)), d))
	}
	var deletes []decisionfold.Delete
	if rng.IntN(4) == 0 {
		deletes = append(deletes, decisionfold.Delete{UID: pick("t-1", "t-p"), Cascade: rng.IntN(2) == 0})
	}
	return events, deletes
}

func TestFold_RandomEventSets_AreOrderIndependent(t *testing.T) {
	const universes = 150
	for seed := uint64(1); seed <= universes; seed++ {
		rng := rand.New(rand.NewPCG(seed, 77))
		events, deletes := soup(t, rng)
		for _, id := range []string{"r1", "r2", "r3", "r4", "r1x"} {
			want := decisionfold.Fold(id, events, deletes)
			permutations(seed, 8, events, func(label string, perm []decisionfold.Event) {
				got := decisionfold.Fold(id, perm, deletes)
				require.Equal(t, want, got, "request %s, universe seed %d, %s", id, seed, label)
			})
			if want.Present {
				assert.Contains(t, []decisionfold.State{decisionfold.StateOpen, decisionfold.StateConflicted, decisionfold.StateAnswered,
					decisionfold.StateApplied, decisionfold.StateWithdrawn, decisionfold.StateSuperseded, decisionfold.StateInvalid}, want.State)
			}
		}
	}
}
