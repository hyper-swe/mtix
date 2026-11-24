// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type fieldCase struct {
	kind, field string
	good, bad   any
}

func repeatedDecisionItems(v any, n int) []any {
	a := make([]any, n)
	for i := range a {
		a[i] = v
	}
	return a
}

func decisionMutation(t *testing.T, kind, path string, value any) []byte {
	t.Helper()
	var root map[string]any
	require.NoError(t, json.Unmarshal(decisionFixture(t, kind), &root))
	m := root
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		switch cur := m[k].(type) {
		case []any:
			m = cur[0].(map[string]any)
		case map[string]any:
			m = cur
		default:
			child := map[string]any{}
			m[k] = child
			m = child
		}
	}
	m[parts[len(parts)-1]] = value
	b, err := json.Marshal(root)
	require.NoError(t, err)
	return b
}

func TestDecisionFields_RequestOptions(t *testing.T) {
	rep := strings.Repeat
	options := func(n int) []any {
		a := make([]any, n)
		for i := range a {
			a[i] = map[string]any{"id": string(rune('a' + i)), "label": "A"}
		}
		return a
	}
	link := map[string]string{"kind": "future", "label": "page", "url": "http://[::1]:8000"}
	verifyDecisionFields(t, []fieldCase{
		{"request", "v", 1, 0},
		{"request", "kind", "request", 12},
		{"request", "request_key", rep("A", 64), rep("a", 65)},
		{"request", "request_key", "_", ":"},
		{"request", "question", rep("界", 300), rep("x", 301)},
		{"request", "question", "?", ""},
		{"request", "question", "ok", "a\nb"},
		{"request", "context", rep("é", 16384), rep("é", 16385)},
		{"request", "options", options(9), options(10)},
		{"request", "options.id", rep("a", 32), "A"},
		{"request", "options.id", "a", rep("a", 33)},
		{"request", "options", options(2), repeatedDecisionItems(options(1)[0], 2)},
		{"request", "options.label", rep("界", 80), rep("a", 81)},
		{"request", "options.consequence", rep("a", 160), rep("a", 161)},
		{"request", "options.consequence", "plain", "a\nb"},
		{"request", "options.input.required", false, "true"},
		{"request", "options.note_required", true, 1},
		{"request", "options.input.type", "url", false},
		{"request", "recommend", map[string]string{"option": "yes", "reason": rep("a", 300)}, map[string]string{"option": "no"}},
		{"request", "recommend", map[string]string{"option": "yes"}, map[string]string{"option": "yes", "reason": rep("界", 301)}},
		{"request", "links", repeatedDecisionItems(link, 10), repeatedDecisionItems(link, 11)},
		{"request", "links.kind", "novel", true},
		{"request", "links.label", rep("a", 80), rep("a", 81)},
		{"request", "links.url", "http://localhost:7", "http://example.com"},
		{"request", "links.url", "http://127.0.0.1", "http://127.0.0.2"},
		{"request", "links.url", "https://example.com", "javascript:alert(1)"},
	})
}

func TestDecisionFields_RequestMetadata(t *testing.T) {
	const event = "01912345-6789-7abc-8123-123456789abc"
	rep := strings.Repeat
	ticket := func(n int) any {
		return map[string]any{"uid": "u", "ancestors": repeatedDecisionItems("", n), "paths": repeatedDecisionItems("T-1", n+1), "path_match": repeatedDecisionItems(true, n+1), "truncated": false}
	}
	source := func(n int) any {
		m := map[string]string{}
		for i := range n {
			m[string(rune('a'+i))] = rep("界", 120)
		}
		return m
	}
	verifyDecisionFields(t, []fieldCase{
		{"request", "links.required", false, nil},
		{"request", "links.open_before_answer", true, 1},
		{"request", "deadline", "2026-10-30T12:00:00Z", "2026-10-30T12:00:00+05:00"},
		{"request", "answerable_by", "any", 1},
		{"request", "requires_outcome", false, nil},
		{"request", "answerers", repeatedDecisionItems("bob", 8), repeatedDecisionItems("bob", 9)},
		{"request", "answerers", []string{"b"}, []string{"asker"}},
		{"request", "answerers", []string{rep("a", 64)}, []string{"A"}},
		{"request", "key", rep(":", 128), rep("a", 129)},
		{"request", "key", "a", " "},
		{"request", "supersedes", repeatedDecisionItems(event, 4), repeatedDecisionItems(event, 5)},
		{"request", "category", rep("z", 32), "Bad!"},
		{"request", "category", rep("z", 32), rep("z", 33)},
		{"request", "source", source(8), source(9)},
		{"request", "source", map[string]string{rep("a", 24): ""}, map[string]string{rep("a", 25): ""}},
		{"request", "source", map[string]string{"a": rep("a", 120)}, map[string]string{"a": rep("界", 121)}},
		{"request", "ticket", ticket(64), ticket(65)},
		{"request", "ticket.uid", "u", ""},
		{"request", "ticket.paths", []string{"A", "B"}, []string{"A"}},
		{"request", "ticket.path_match", []bool{false, true}, []bool{false}},
		{"request", "ticket.truncated", true, 0},
		{"request", "asker", map[string]string{"kind": "agent", "model": rep("a", 80)}, map[string]string{"kind": "user", "model": rep("a", 81)}},
		{"answer", "target", event, ""},
	})
}

func TestDecisionFields_Events(t *testing.T) {
	const event = "01912345-6789-7abc-8123-123456789abc"
	rep := strings.Repeat
	att := map[string]string{"frontend": "app", "user": "me", "clicked_at": "2026-11-21T00:00:00Z"}
	verifyDecisionFields(t, []fieldCase{
		{"answer", "option", "yes", "A"},
		{"answer", "input_value", "note", 42},
		{"answer", "note", rep("a", 16384), rep("a", 16385)},
		{"answer", "decider.kind", "agent", 1},
		{"answer", "decider.model", rep("a", 80), rep("a", 81)},
		{"answer", "channel", "novel", "frontend:app"},
		{"answer", "attestation", map[string]string{"frontend": "app", "user": rep("界", 120), "clicked_at": att["clicked_at"]}, map[string]string{"frontend": "app", "user": rep("界", 121), "clicked_at": att["clicked_at"]}},
		{"answer", "attestation", att, map[string]string{"frontend": "app", "user": "me", "clicked_at": "bad"}},
		{"answer", "evidence.ancestors", repeatedDecisionItems("p", 8), repeatedDecisionItems("p", 9)},
		{"answer", "evidence.markers_unset", []string{}, []int{1}},
		{"answer", "opened_links", []int{0}, []int{-1}},
		{"outcome", "answer", event, ""},
		{"refused", "seen_answers", repeatedDecisionItems(event, 32), repeatedDecisionItems(event, 33)},
		{"outcome", "result", "applied", "refused"},
		{"outcome", "receipt.seq", 0, -1},
		{"outcome", "receipt.hash", rep("a", 128), rep("b", 129)},
		{"outcome", "receipt.hash", "ab", "gh"},
		{"outcome", "receipt.ledger", rep("a", 64), rep("a", 65)},
		{"outcome", "receipt.run", rep("a", 128), rep("a", 129)},
		{"outcome", "receipt.kind", rep("a", 32), rep("a", 33)},
		{"outcome", "receipt.at", att["clicked_at"], "bad"},
		{"withdraw", "reason_code", "future", 1},
		{"withdraw", "reason", rep("a", 2048), rep("a", 2049)},
		{"answered-elsewhere", "elsewhere", map[string]any{"channel": rep("a", 64), "decider": map[string]string{"kind": "user"}, "receipt": map[string]any{}}, map[string]any{"channel": rep("a", 65), "decider": map[string]string{"kind": "user"}, "receipt": map[string]any{}}},
	})
}

func verifyDecisionFields(t *testing.T, cases []fieldCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.field, func(t *testing.T) {
			for _, v := range []struct {
				value any
				valid bool
			}{{tc.good, true}, {tc.bad, false}} {
				_, err := DecodeDecision(decisionMutation(t, tc.kind, tc.field, v.value), "asker", false)
				if v.valid {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrInvalidInput)
				}
			}
		})
	}
}

func TestDecisionValidation_UnsupportedAndMissing(t *testing.T) {
	for _, tc := range []fieldCase{{"request", "v", nil, 2},
		{"request", "kind", nil, "future"},
		{"request", "answerable_by", nil, "future"},
		{"request", "options.input.type", nil, "future"},
		{"answer", "decider.kind", nil, "future"},
		{"outcome", "result", nil, "future"}} {
		_, err := DecodeDecision(decisionMutation(t, tc.kind, tc.field, tc.bad), "asker", false)
		require.ErrorIs(t, err, ErrDecisionUnsupported)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	for k, fields := range map[string][]string{"request": {"v", "kind", "request_key", "ticket", "question"}, "answer": {"target", "decider", "channel"}, "outcome": {"answer", "result"}, "withdraw": {"target"}} {
		for _, f := range fields {
			var m map[string]any
			require.NoError(t, json.Unmarshal(decisionFixture(t, k), &m))
			delete(m, f)
			b, err := json.Marshal(m)
			require.NoError(t, err)
			_, err = DecodeDecision(b, "asker", false)
			require.ErrorIs(t, err, ErrInvalidInput, f)
		}
	}
}

func TestDecisionAnswer_RequestRules(t *testing.T) {
	req, err := DecodeDecision(decisionFixture(t, "request"), "asker", false)
	require.NoError(t, err)
	for _, tc := range []struct {
		typ, option, input, note string
		opened                   []int
		valid                    bool
	}{
		{"text", "yes", " e\u0301 ", " ok ", []int{0}, true},
		{"text", "no", "a", "a", []int{0}, false},
		{"text", "yes", " ", "a", []int{0}, false},
		{"text", "yes", "a", " ", []int{0}, false},
		{"text", "yes", "x", "y", nil, false},
		{"text", "yes", "x", "y", []int{0, 1}, false},
		{"finding_id", "yes", "f:1", "y", []int{0}, true},
		{"story_ref", "yes", "bad id", "y", []int{0}, false},
		{"url", "yes", "https://host", "y", []int{0}, true},
		{"url", "yes", "file:///etc", "y", []int{0}, false},
	} {
		req.Payload.Options[0].Input.Type = tc.typ
		a := DecisionPayload{DecisionAnswer: &DecisionAnswer{Option: tc.option, InputValue: tc.input, Note: tc.note, OpenedLinks: tc.opened}}
		err := ValidateDecisionAnswer(req.Payload, a, trimOnly)
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrInvalidInput)
		}
	}
}
