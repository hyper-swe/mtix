// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestIngestDecision_AnswerFields_Boundaries(t *testing.T) {
	v, m, u := model.DecisionValid, model.DecisionMalformed, model.DecisionUnsupported
	att := obj{"frontend": "slackish", "user": "alice", "clicked_at": "2026-10-30T12:00:00+02:00"}
	idx := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	names := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "proc"
		}
		return out
	}
	runFieldCases(t, baseAnswer, []fieldCase{
		{"base is valid", "x", nil, v},
		{"target missing", "target", nil, m},
		{"target empty", "target", "", m},
		{"target bad grammar", "target", "a b", m},
		{"target at cap", "target", rep("t", 64), v},
		{"target over cap", "target", rep("t", 65), m},
		{"request_key missing", "request_key", nil, m},
		{"option absent", "option", nil, v},
		{"option bad grammar", "option", "Approve", m},
		{"option wrong type", "option", 1, m},
		{"input_value at cap", "input_value", rep("i", 16*1024), v},
		{"input_value over cap", "input_value", rep("i", 16*1024+1), m},
		{"input_value wrong type", "input_value", 5, m},
		{"note at cap", "note", rep("n", 16*1024), v},
		{"note over cap", "note", rep("n", 16*1024+1), m},
		{"note wrong type", "note", true, m},
		{"decider missing", "decider", nil, m},
		{"decider agent", "decider", obj{"kind": "agent", "model": "m"}, v},
		{"decider unknown kind", "decider", obj{"kind": "daemon"}, u},
		{"decider kind missing", "decider", obj{"model": "m"}, m},
		{"decider model over cap", "decider", obj{"kind": "human", "model": rep("m", 81)}, m},
		{"decider wrong type", "decider", "human", m},
		{"channel missing", "channel", nil, m},
		{"channel cli-tty", "channel", "cli-tty", v},
		{"channel mcp", "channel", "mcp", v},
		{"channel api", "channel", "api", v},
		{"channel cli", "channel", "cli", v},
		{"channel unknown kept", "channel", "carrier-pigeon", v},
		{"channel upper case", "channel", "Web", m},
		{"channel over cap", "channel", rep("c", 65), m},
		{"channel wrong type", "channel", 1, m},
		{"frontend without attestation", "channel", "frontend:slackish", m},
		{"frontend without a name", "channel", "frontend:", m},
		{"evidence empty", "evidence", obj{}, v},
		{"evidence full", "evidence", obj{"ancestors": names(8), "tty_session": "s1", "markers_unset": names(32)}, v},
		{"evidence nine ancestors", "evidence", obj{"ancestors": names(9)}, m},
		{"evidence 33 markers", "evidence", obj{"markers_unset": names(33)}, m},
		{"evidence name over cap", "evidence", obj{"ancestors": []any{rep("p", 65)}}, m},
		{"evidence tty over cap", "evidence", obj{"tty_session": rep("s", 129)}, m},
		{"evidence wrong type", "evidence", []any{}, m},
		{"opened_links ten", "opened_links", idx(10), v},
		{"opened_links eleven", "opened_links", idx(11), m},
		{"opened_links index too large", "opened_links", []any{10}, m},
		{"opened_links negative", "opened_links", []any{-1}, m},
		{"opened_links fraction", "opened_links", []any{1.5}, m},
		{"opened_links string", "opened_links", []any{"0"}, m},
		{"opened_links duplicate", "opened_links", []any{0, 0}, m},
		{"attestation on non-frontend channel", "attestation", att, v},
		{"attestation clicked_at garbage", "attestation", obj{"frontend": "f", "user": "u", "clicked_at": "x"}, m},
		{"attestation user over cap", "attestation", obj{"frontend": "f", "user": rep("u", 121), "clicked_at": "2026-10-30T12:00:00Z"}, m},
		{"attestation field missing", "attestation", obj{"frontend": "f"}, m},
	})
	t.Run("frontend channel with attestation", func(t *testing.T) {
		b := withField(withField(baseAnswer(), "channel", "frontend:slackish"), "attestation", att)
		assert.Equal(t, v, ingest(t, b).Class)
	})
}

func TestIngestDecision_OutcomeFields_Boundaries(t *testing.T) {
	v, m, u := model.DecisionValid, model.DecisionMalformed, model.DecisionUnsupported
	refused := func() obj {
		return withField(withField(baseOutcome(), "result", "refused"), "refusal", obj{"text": "no", "code": "stale"})
	}
	ids := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = "a" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		}
		return out
	}
	rc := func(k string, val any) obj { return obj{k: val} }
	runFieldCases(t, baseOutcome, []fieldCase{
		{"base is valid", "x", nil, v},
		{"answer missing", "answer", nil, m},
		{"answer bad grammar", "answer", "a b", m},
		{"result missing", "result", nil, m},
		{"result unknown", "result", "deferred", u},
		{"result wrong type", "result", 1, m},
		{"receipt empty", "receipt", obj{}, v},
		{"receipt full", "receipt", obj{"ledger": "L", "run": "r1", "seq": 3, "hash": "ab12", "kind": "k", "at": "2026-10-30T12:00:00Z"}, v},
		{"receipt ledger over cap", "receipt", rc("ledger", rep("l", 65)), m},
		{"receipt run over cap", "receipt", rc("run", rep("r", 129)), m},
		{"receipt run at cap", "receipt", rc("run", rep("r", 128)), v},
		{"receipt seq negative", "receipt", rc("seq", -1), m},
		{"receipt seq fraction", "receipt", rc("seq", 1.5), m},
		{"receipt seq string", "receipt", rc("seq", "1"), m},
		{"receipt seq zero", "receipt", rc("seq", 0), v},
		{"receipt hash not hex", "receipt", rc("hash", "xyz"), m},
		{"receipt hash at cap", "receipt", rc("hash", rep("a", 128)), v},
		{"receipt hash over cap", "receipt", rc("hash", rep("a", 129)), m},
		{"receipt hash empty", "receipt", rc("hash", ""), m},
		{"receipt kind over cap", "receipt", rc("kind", rep("k", 33)), m},
		{"receipt at garbage", "receipt", rc("at", "x"), m},
		{"receipt wrong type", "receipt", "r", m},
		{"seen_answers 32", "seen_answers", ids(32), v},
		{"seen_answers 33", "seen_answers", ids(33), m},
		{"seen_answers duplicate", "seen_answers", []any{"a", "a"}, m},
		{"seen_answers bad entry", "seen_answers", []any{""}, m},
	})
	t.Run("refused is valid with a refusal", func(t *testing.T) {
		assert.Equal(t, v, ingest(t, refused()).Class)
	})
	runFieldCases(t, refused, []fieldCase{
		{"refusal missing", "refusal", nil, m},
		{"refusal text missing", "refusal", obj{"code": "c"}, m},
		{"refusal text empty", "refusal", obj{"text": ""}, m},
		{"refusal text at cap", "refusal", obj{"text": rep("t", 16*1024)}, v},
		{"refusal text over cap", "refusal", obj{"text": rep("t", 16*1024+1)}, m},
		{"refusal text multi line", "refusal", obj{"text": "a\nb"}, v},
		{"refusal code at cap", "refusal", obj{"text": "x", "code": rep("c", 64)}, v},
		{"refusal code over cap", "refusal", obj{"text": "x", "code": rep("c", 65)}, m},
		{"refusal wrong type", "refusal", "no", m},
	})
}

func TestIngestDecision_OutcomeSeenAnswers_DefaultsToAnswer(t *testing.T) {
	res := ingest(t, baseOutcome())
	require.Equal(t, model.DecisionValid, res.Class, res.Reason)
	assert.Equal(t, []string{"evt-2"}, res.Value.Outcome.SeenAnswers)
	res = ingest(t, withField(baseOutcome(), "seen_answers", []any{"x", "y"}))
	assert.Equal(t, []string{"x", "y"}, res.Value.Outcome.SeenAnswers)
}

func TestIngestDecision_WithdrawFields_Boundaries(t *testing.T) {
	v, m, u := model.DecisionValid, model.DecisionMalformed, model.DecisionUnsupported
	el := func(decider any) obj {
		return obj{"channel": "web", "decider": decider, "receipt": obj{"run": "r"}}
	}
	runFieldCases(t, baseWithdraw, []fieldCase{
		{"base is valid", "x", nil, v},
		{"target missing", "target", nil, m},
		{"reason_code moot", "reason_code", "moot", v},
		{"reason_code answered_elsewhere", "reason_code", "answered_elsewhere", v},
		{"reason_code unknown kept", "reason_code", "cosmic_rays", v},
		{"reason_code bad grammar", "reason_code", "Not Valid", m},
		{"reason_code wrong type", "reason_code", 1, m},
		{"reason at cap", "reason", rep("r", 2048), v},
		{"reason over cap", "reason", rep("r", 2049), m},
		{"reason wrong type", "reason", 1, m},
		{"elsewhere valid", "elsewhere", el(obj{"kind": "human"}), v},
		{"elsewhere channel over cap", "elsewhere", obj{"channel": rep("c", 65), "decider": obj{"kind": "human"}}, m},
		{"elsewhere decider unknown kind", "elsewhere", el(obj{"kind": "ghost"}), u},
		{"elsewhere receipt bad", "elsewhere", obj{"decider": obj{"kind": "human"}, "receipt": obj{"seq": -2}}, m},
		{"elsewhere wrong type", "elsewhere", "web", m},
	})
	t.Run("reason_code defaults to other", func(t *testing.T) {
		res := ingest(t, baseWithdraw())
		assert.Equal(t, "other", res.Value.Withdraw.ReasonCode)
	})
}

func TestIngestDecision_Unsupported_VersionAndKind(t *testing.T) {
	tests := []struct {
		name string
		obj  obj
		want model.DecisionClass
		kind model.DecisionKind
	}{
		{"future version of a request", withField(baseRequest(), "v", 2), model.DecisionUnsupported, "request"},
		{"future version of an answer with a changed shape", obj{"v": 3, "kind": "answer", "target": 5}, model.DecisionUnsupported, "answer"},
		{"unknown kind", withField(baseRequest(), "kind", "vote"), model.DecisionUnsupported, "vote"},
		{"unknown enum plus a malformed field is malformed", withField(withField(baseRequest(), "answerable_by", "robot"), "question", ""), model.DecisionMalformed, "request"},
		{"not an object", nil, model.DecisionMalformed, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res model.DecisionIngest
			if tt.obj == nil {
				res = model.IngestDecision([]byte(`[1,2]`))
			} else {
				res = ingest(t, tt.obj)
			}
			assert.Equal(t, tt.want, res.Class, res.Reason)
			assert.Equal(t, tt.kind, res.Kind)
			assert.Nil(t, res.Value)
			assert.NotEmpty(t, res.Reason)
		})
	}
}

func TestIngestDecision_UnknownFields_AreIgnoredButHashed(t *testing.T) {
	a := ingest(t, baseRequest())
	b := ingest(t, withField(baseRequest(), "extra", obj{"x": []any{1, 2}}))
	require.Equal(t, model.DecisionValid, b.Class, b.Reason)
	assert.NotEqual(t, a.PayloadSHA256, b.PayloadSHA256)
	assert.Equal(t, a.Value, b.Value)
}

func TestIngestDecision_BadBytes_AreMalformedNotErrors(t *testing.T) {
	big := `{"v":1,"kind":"request","question":"` + rep("q", 70*1024) + `"}`
	tests := []struct{ name, in string }{
		{"empty", ``},
		{"truncated", `{"v":1,`},
		{"number overflow", `{"v":1e999}`},
		{"oversize", big},
		{"too deep", `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":{"j":1}}}}}}}}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := model.IngestDecision([]byte(tt.in))
			assert.Equal(t, model.DecisionMalformed, res.Class)
			assert.Nil(t, res.Value)
			assert.Empty(t, res.PayloadSHA256)
		})
	}
}
