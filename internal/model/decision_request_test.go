// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type fieldCase struct {
	name  string
	field string
	value any // nil deletes the field
	want  model.DecisionClass
}

func runFieldCases(t *testing.T, base func() obj, cases []fieldCase) {
	t.Helper()
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			res := ingest(t, withField(base(), tt.field, tt.value))
			assert.Equal(t, tt.want, res.Class, "reason: %s", res.Reason)
			if tt.want == model.DecisionValid {
				require.NotNil(t, res.Value)
			} else {
				assert.Nil(t, res.Value)
			}
		})
	}
}

func opt(id string) obj { return obj{"id": id} }

func TestIngestDecision_RequestScalarFields_Boundaries(t *testing.T) {
	v, m, u := model.DecisionValid, model.DecisionMalformed, model.DecisionUnsupported
	runFieldCases(t, baseRequest, []fieldCase{
		{"base is valid", "x", nil, v},
		{"v missing", "v", nil, m},
		{"v zero", "v", 0, m},
		{"v negative", "v", -1, m},
		{"v fraction", "v", 1.5, m},
		{"v string", "v", "1", m},
		{"v two", "v", 2, u},
		{"v huge", "v", 1e300, m},
		{"kind missing", "kind", nil, m},
		{"kind number", "kind", 3, m},
		{"kind unknown", "kind", "poll", u},
		{"request_key missing", "request_key", nil, m},
		{"request_key empty", "request_key", "", m},
		{"request_key at cap", "request_key", rep("k", 64), v},
		{"request_key over cap", "request_key", rep("k", 65), m},
		{"request_key bad grammar", "request_key", "a b", m},
		{"request_key wrong type", "request_key", 7, m},
		{"question missing", "question", nil, m},
		{"question empty", "question", "", m},
		{"question at cap", "question", rep("q", 300), v},
		{"question over cap", "question", rep("q", 301), m},
		{"question multibyte at cap", "question", rep("é", 300), v},
		{"question line break", "question", "a\nb", m},
		{"question carriage return", "question", "a\rb", m},
		{"question line separator", "question", "a b", m},
		{"question control char", "question", "a\x00b", m},
		{"question wrong type", "question", []any{"a"}, m},
		{"context at cap", "context", rep("c", 32*1024), v},
		{"context over cap", "context", rep("c", 32*1024+1), m},
		{"context multi line", "context", "a\n\nb\tc", v},
		{"context wrong type", "context", 1, m},
		{"deadline utc", "deadline", "2026-10-30T12:00:00Z", v},
		{"deadline offset", "deadline", "2026-10-30T12:00:00+02:00", m},
		{"deadline garbage", "deadline", "tomorrow", m},
		{"deadline wrong type", "deadline", 5, m},
		{"answerable_by any", "answerable_by", "any", v},
		{"answerable_by human", "answerable_by", "human", v},
		{"answerable_by unknown", "answerable_by", "robot", u},
		{"answerable_by wrong type", "answerable_by", true, m},
		{"requires_outcome false", "requires_outcome", false, v},
		{"requires_outcome wrong type", "requires_outcome", "yes", m},
		{"key at cap", "key", rep("k", 128), v},
		{"key over cap", "key", rep("k", 129), m},
		{"key empty", "key", "", m},
		{"key allowed punctuation", "key", "app1:1a2b.c/r-5_x", v},
		{"key bad grammar", "key", "a b", m},
		{"category ok", "category", "gate-2", v},
		{"category at cap", "category", rep("c", 32), v},
		{"category over cap", "category", rep("c", 33), m},
		{"category upper case", "category", "Gate", m},
		{"asker agent", "asker", obj{"kind": "agent", "model": "x"}, v},
		{"asker model at cap", "asker", obj{"kind": "human", "model": rep("m", 80)}, v},
		{"asker model over cap", "asker", obj{"kind": "human", "model": rep("m", 81)}, m},
		{"asker kind unknown", "asker", obj{"kind": "service"}, m},
		{"asker wrong type", "asker", "agent", m},
		{"unknown field ignored", "future_field", obj{"a": 1}, v},
	})
}

func TestIngestDecision_RequestDefaults_AreApplied(t *testing.T) {
	res := ingest(t, baseRequest())
	require.Equal(t, model.DecisionValid, res.Class, res.Reason)
	r := res.Value.Request
	assert.Equal(t, "human", r.AnswerableBy)
	assert.True(t, r.RequiresOutcome)
	assert.Equal(t, "agent", r.Asker.Kind)
	assert.Empty(t, r.Answerers)
	assert.Empty(t, r.Supersedes)
	assert.Empty(t, r.Links)
	assert.Empty(t, r.Source)
	assert.Empty(t, r.Context)
	assert.Equal(t, model.DecisionKindRequest, res.Value.Kind)
}

func TestIngestDecision_RequestWithoutOptions_IsFreeTextQuestion(t *testing.T) {
	res := ingest(t, withField(baseRequest(), "options", nil))
	require.Equal(t, model.DecisionValid, res.Class, res.Reason)
	assert.Empty(t, res.Value.Request.Options)
}

func TestIngestDecision_Options_Boundaries(t *testing.T) {
	v, m, u := model.DecisionValid, model.DecisionMalformed, model.DecisionUnsupported
	nine := make([]any, 9)
	ten := make([]any, 10)
	for i := range ten {
		ten[i] = opt("o" + string(rune('a'+i)))
		if i < 9 {
			nine[i] = ten[i]
		}
	}
	in := func(typ string) obj { return obj{"id": "x", "input": obj{"type": typ, "required": true}} }
	runFieldCases(t, baseRequest, []fieldCase{
		{"empty list", "options", []any{}, v},
		{"nine options", "options", nine, v},
		{"ten options", "options", ten, m},
		{"not a list", "options", obj{}, m},
		{"entry not an object", "options", []any{"approve"}, m},
		{"duplicate ids", "options", []any{opt("a"), opt("a")}, m},
		{"id missing", "options", []any{obj{"label": "x"}}, m},
		{"id empty", "options", []any{opt("")}, m},
		{"id at cap", "options", []any{opt(rep("a", 32))}, v},
		{"id over cap", "options", []any{opt(rep("a", 33))}, m},
		{"id upper case", "options", []any{opt("Approve")}, m},
		{"id wrong type", "options", []any{obj{"id": 1}}, m},
		{"label at cap", "options", []any{obj{"id": "a", "label": rep("l", 80)}}, v},
		{"label over cap", "options", []any{obj{"id": "a", "label": rep("l", 81)}}, m},
		{"consequence at cap", "options", []any{obj{"id": "a", "consequence": rep("c", 160)}}, v},
		{"consequence over cap", "options", []any{obj{"id": "a", "consequence": rep("c", 161)}}, m},
		{"consequence line break", "options", []any{obj{"id": "a", "consequence": "x\ny"}}, m},
		{"note_required true", "options", []any{obj{"id": "a", "note_required": true}}, v},
		{"note_required wrong type", "options", []any{obj{"id": "a", "note_required": "y"}}, m},
		{"input text", "options", []any{in("text")}, v},
		{"input finding_id", "options", []any{in("finding_id")}, v},
		{"input story_ref", "options", []any{in("story_ref")}, v},
		{"input url", "options", []any{in("url")}, v},
		{"input unknown type", "options", []any{in("file")}, u},
		{"input type missing", "options", []any{obj{"id": "a", "input": obj{"required": true}}}, m},
		{"input required wrong type", "options", []any{obj{"id": "a", "input": obj{"type": "text", "required": 1}}}, m},
		{"input not an object", "options", []any{obj{"id": "a", "input": "text"}}, m},
		{"unknown type and bad field is malformed", "options",
			[]any{obj{"id": "A", "input": obj{"type": "file"}}}, m},
	})
}

func TestIngestDecision_Recommend_Boundaries(t *testing.T) {
	v, m := model.DecisionValid, model.DecisionMalformed
	runFieldCases(t, baseRequest, []fieldCase{
		{"existing option", "recommend", obj{"option": "approve", "reason": "ok"}, v},
		{"reason at cap", "recommend", obj{"option": "approve", "reason": rep("r", 300)}, v},
		{"reason over cap", "recommend", obj{"option": "approve", "reason": rep("r", 301)}, m},
		{"option missing", "recommend", obj{"reason": "ok"}, m},
		{"option not listed", "recommend", obj{"option": "other"}, m},
		{"wrong type", "recommend", "approve", m},
	})
	t.Run("recommend with no options is malformed", func(t *testing.T) {
		b := withField(withField(baseRequest(), "options", nil), "recommend", obj{"option": "approve"})
		assert.Equal(t, m, ingest(t, b).Class)
	})
}

func TestIngestDecision_Links_Boundaries(t *testing.T) {
	v, m := model.DecisionValid, model.DecisionMalformed
	link := func(u string) obj { return obj{"kind": "review", "url": u, "label": "L"} }
	ten := make([]any, 10)
	eleven := make([]any, 11)
	for i := range eleven {
		eleven[i] = link("https://example.com/" + string(rune('a'+i)))
		if i < 10 {
			ten[i] = eleven[i]
		}
	}
	runFieldCases(t, baseRequest, []fieldCase{
		{"https", "links", []any{link("https://example.com/x")}, v},
		{"http loopback", "links", []any{link("http://127.0.0.1:7300/#review")}, v},
		{"http ipv6 loopback", "links", []any{link("http://[::1]:80/")}, v},
		{"http localhost", "links", []any{link("http://localhost:8080/a")}, v},
		{"http other host", "links", []any{link("http://example.com/")}, m},
		{"http lookalike host", "links", []any{link("http://localhost.evil.com/")}, m},
		{"javascript", "links", []any{link("javascript:alert(1)")}, m},
		{"data", "links", []any{link("data:text/html,x")}, m},
		{"file", "links", []any{link("file:///etc/passwd")}, m},
		{"user info", "links", []any{link("https://u:p@example.com/")}, m},
		{"user info disguised host", "links", []any{link("http://127.0.0.1@evil.com/")}, m},
		{"scheme only", "links", []any{link("https:")}, m},
		{"no scheme", "links", []any{link("example.com")}, m},
		{"upper case scheme", "links", []any{link("HTTPS://example.com/")}, v},
		{"space in url", "links", []any{link("https://example.com/a b")}, m},
		{"url missing", "links", []any{obj{"kind": "pr"}}, m},
		{"url empty", "links", []any{link("")}, m},
		{"ten links", "links", ten, v},
		{"eleven links", "links", eleven, m},
		{"unknown kind kept", "links", []any{obj{"kind": "weird", "url": "https://example.com/"}}, v},
		{"kind bad grammar", "links", []any{obj{"kind": "Not Valid", "url": "https://example.com/"}}, m},
		{"label at cap", "links", []any{obj{"url": "https://e.com/", "label": rep("l", 80)}}, v},
		{"label over cap", "links", []any{obj{"url": "https://e.com/", "label": rep("l", 81)}}, m},
		{"required wrong type", "links", []any{obj{"url": "https://e.com/", "required": "y"}}, m},
		{"open_before_answer wrong type", "links", []any{obj{"url": "https://e.com/", "open_before_answer": 1}}, m},
		{"not a list", "links", "https://example.com/", m},
	})
	t.Run("kind defaults to other", func(t *testing.T) {
		res := ingest(t, withField(baseRequest(), "links", []any{obj{"url": "https://e.com/"}}))
		require.Equal(t, v, res.Class, res.Reason)
		assert.Equal(t, "other", res.Value.Request.Links[0].Kind)
	})
}

func TestIngestDecision_ListsAndMaps_Boundaries(t *testing.T) {
	v, m := model.DecisionValid, model.DecisionMalformed
	ids := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "agent-" + string(rune('a'+i))
		}
		return out
	}
	srcN := func(n int) obj {
		out := obj{}
		for i := range n {
			out["k"+string(rune('a'+i))] = "v"
		}
		return out
	}
	runFieldCases(t, baseRequest, []fieldCase{
		{"answerers eight", "answerers", ids(8), v},
		{"answerers nine", "answerers", ids(9), m},
		{"answerers bad grammar", "answerers", []any{"Alice"}, m},
		{"answerers empty entry", "answerers", []any{""}, m},
		{"answerers duplicate", "answerers", []any{"alice", "alice"}, m},
		{"answerers wrong entry type", "answerers", []any{1}, m},
		{"answerers not a list", "answerers", "alice", m},
		{"supersedes four", "supersedes", []any{"a", "b", "c", "d"}, v},
		{"supersedes five", "supersedes", []any{"a", "b", "c", "d", "e"}, m},
		{"supersedes duplicate", "supersedes", []any{"a", "a"}, m},
		{"supersedes bad id", "supersedes", []any{"a b"}, m},
		{"source eight pairs", "source", srcN(8), v},
		{"source nine pairs", "source", srcN(9), m},
		{"source key bad grammar", "source", obj{"Bad-Key": "v"}, m},
		{"source key at cap", "source", obj{rep("a", 24): "v"}, v},
		{"source key over cap", "source", obj{rep("a", 25): "v"}, m},
		{"source value at cap", "source", obj{"app": rep("v", 120)}, v},
		{"source value over cap", "source", obj{"app": rep("v", 121)}, m},
		{"source value wrong type", "source", obj{"app": 1}, m},
		{"source value null", "source", obj{"app": nil}, m},
		{"source value line break", "source", obj{"app": "a\nb"}, m},
		{"source not an object", "source", []any{}, m},
	})
}

func TestIngestDecision_Ticket_ShapeOnly(t *testing.T) {
	v, m := model.DecisionValid, model.DecisionMalformed
	anc := func(n int) (a, p, pm []any) {
		p, pm = []any{"P-0"}, []any{true}
		for i := range n {
			a = append(a, "u"+string(rune('a'+i%26))+string(rune('a'+i/26)))
			p = append(p, "P")
			pm = append(pm, false)
		}
		return a, p, pm
	}
	a64, p64, m64 := anc(64)
	a65, p65, m65 := anc(65)
	tk := func(uid string, a, p, pm []any) obj {
		return obj{"uid": uid, "ancestors": a, "paths": p, "path_match": pm, "truncated": true}
	}
	runFieldCases(t, baseRequest, []fieldCase{
		{"missing ticket", "ticket", nil, m},
		{"ticket not an object", "ticket", "u1", m},
		{"no ancestors", "ticket", tk("u1", []any{}, []any{"P"}, []any{true}), v},
		{"ancestors omitted", "ticket", obj{"uid": "u1", "paths": []any{"P"}, "path_match": []any{false}}, v},
		{"sixty four ancestors", "ticket", tk("u1", a64, p64, m64), v},
		{"sixty five ancestors", "ticket", tk("u1", a65, p65, m65), m},
		{"uid missing", "ticket", obj{"ancestors": []any{}, "paths": []any{"P"}, "path_match": []any{true}}, m},
		{"uid empty is shape valid", "ticket", tk("", []any{}, []any{"P"}, []any{true}), v},
		{"ancestor with empty uid is a missing link", "ticket", tk("u1", []any{""}, []any{"P", "Q"}, []any{true, false}), v},
		{"uid at cap", "ticket", tk(rep("u", 64), []any{}, []any{"P"}, []any{true}), v},
		{"uid over cap", "ticket", tk(rep("u", 65), []any{}, []any{"P"}, []any{true}), m},
		{"paths too short", "ticket", tk("u1", []any{"u0"}, []any{"P"}, []any{true, true}), m},
		{"paths too long", "ticket", tk("u1", []any{}, []any{"P", "Q"}, []any{true}), m},
		{"path_match too short", "ticket", tk("u1", []any{"u0"}, []any{"P", "Q"}, []any{true}), m},
		{"path_match wrong entry", "ticket", tk("u1", []any{}, []any{"P"}, []any{"yes"}), m},
		{"path at cap", "ticket", tk("u1", []any{}, []any{rep("p", 256)}, []any{true}), v},
		{"path over cap", "ticket", tk("u1", []any{}, []any{rep("p", 257)}, []any{true}), m},
		{"paths missing", "ticket", obj{"uid": "u1", "path_match": []any{true}}, m},
		{"truncated wrong type", "ticket", obj{"uid": "u1", "paths": []any{"P"}, "path_match": []any{true}, "truncated": "x"}, m},
	})
}

func TestIngestDecision_TicketUID_IsNotComparedWithAnyEnvelope(t *testing.T) {
	// Validity is shape only: the ticket uid is accepted whatever it is.
	for _, uid := range []string{"u1", "not-a-uuid", "00000000-0000-0000-0000-000000000000"} {
		b := baseRequest()
		b["ticket"] = obj{"uid": uid, "paths": []any{"P"}, "path_match": []any{true}}
		assert.Equal(t, model.DecisionValid, ingest(t, b).Class)
	}
}

func TestValidateDecisionRequestAuthor_AnswerersContainAsker_Refused(t *testing.T) {
	res := ingest(t, withField(baseRequest(), "answerers", []any{"alice", "agent-1"}))
	require.Equal(t, model.DecisionValid, res.Class, res.Reason)
	require.ErrorIs(t, model.ValidateDecisionRequestAuthor(res.Value.Request, "agent-1"), model.ErrInvalidInput)
	require.NoError(t, model.ValidateDecisionRequestAuthor(res.Value.Request, "agent-2"))
	require.NoError(t, model.ValidateDecisionRequestAuthor(nil, "agent-2"))
}
