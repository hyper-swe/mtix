// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecisionIngest_InvalidClassification(t *testing.T) {
	for _, tc := range []struct {
		kind, field string
		value       any
		unsupported bool
	}{
		{"request", "v", 2, true},
		{"answer", "v", 2, true},
		{"withdraw", "v", 2, true},
		{"outcome", "v", 2, true},
		{"request", "answerable_by", "robot", true},
		{"answer", "decider.kind", "robot", true},
		{"outcome", "result", "future", true},
		{"request", "ticket", nil, false},
		{"answer", "note", false, false},
	} {
		b := decisionMutation(t, tc.kind, tc.field, tc.value)
		d, err := DecodeDecision(b, "asker", false)
		require.ErrorIs(t, err, ErrInvalidInput)
		if tc.unsupported {
			require.ErrorIs(t, err, ErrDecisionUnsupported)
		}
		require.Equal(t, tc.kind, d.Payload.Kind)
		if tc.kind != "request" {
			require.NotEmpty(t, d.Payload.Target)
		}
	}
}

func TestDecisionJSON_EnvelopeLimits(t *testing.T) {
	for _, depth := range []int{9, 10, 11} {
		b := `{"x":` + strings.Repeat("[", depth-1) + "0" + strings.Repeat("]", depth-1) + "}"
		_, _, err := canonicalDecision([]byte(b), false)
		if depth <= 10 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrInvalidInput)
		}
	}
	// Whitespace is not part of the canonical value, so only the raw sanity
	// bound applies to it; the 64 KiB cap is pinned on canonical bytes.
	for _, size := range []int{65535, 65536, 65537, decisionMaxRawBytes} {
		b := append([]byte("{}"), bytes.Repeat([]byte{' '}, size-2)...)
		_, _, err := canonicalDecision(b, false)
		require.NoError(t, err, size)
	}
	_, _, err := canonicalDecision(append([]byte("{}"), bytes.Repeat([]byte{' '}, decisionMaxRawBytes-1)...), false)
	require.ErrorIs(t, err, ErrInvalidInput)
	for _, s := range []string{`{"s":"\u12`, `{"s":"\ud800\u1234"}`, `{"x":1,`, `{"x":[0`} {
		_, _, err := canonicalDecision([]byte(s), false)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	in := `{"s":"\ud83d\ude00\b\t\n\r\f\u0001\"\\"}`
	out, _, err := canonicalDecision([]byte(in), false)
	require.NoError(t, err)
	require.Equal(t, strings.Replace(in, `\ud83d\ude00`, "😀", 1), string(out))
}

func TestDecisionIdentity_OnlyTwoMembersElided(t *testing.T) {
	base, err := DecodeDecision(decisionFixture(t, "request"), "asker", true)
	require.NoError(t, err)
	for _, tc := range []struct {
		field string
		v     any
		same  bool
	}{
		{"request_key", "new", true},
		{"supersedes", []string{}, true},
		{"Future", map[string]string{"request_key": "new"}, false},
		{"QUESTION", false, false},
	} {
		d, err := DecodeDecision(decisionMutation(t, "request", tc.field, tc.v), "asker", true)
		require.NoError(t, err)
		require.NotEqual(t, base.PayloadSHA256, d.PayloadSHA256)
		require.Equal(t, tc.same, base.IdentitySHA256 == d.IdentitySHA256)
		require.Equal(t, "Proceed?", d.Payload.Question)
	}
}

func TestDecisionVariants_RefusalAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		field     string
		good, bad string
	}{{"code", strings.Repeat("c", 64), strings.Repeat("c", 65)},
		{"text", strings.Repeat("é", 8192), strings.Repeat("é", 8193)}} {
		for _, v := range []string{tc.good, tc.bad} {
			r := map[string]string{"text": "verbatim", tc.field: v}
			b := bytes.Replace(decisionMutation(t, "outcome", "refusal", r), []byte(`"applied"`), []byte(`"refused"`), 1)
			d, err := DecodeDecision(b, "asker", true)
			if v == tc.good {
				require.NoError(t, err)
				require.Equal(t, []string{d.Payload.Answer}, d.Payload.SeenAnswers)
			} else {
				require.ErrorIs(t, err, ErrInvalidInput)
			}
		}
	}
	w, err := DecodeDecision(decisionFixture(t, "withdraw"), "asker", false)
	require.NoError(t, err)
	require.Equal(t, "other", w.Payload.ReasonCode)
	r, err := DecodeDecision(decisionMutation(t, "request", "requires_outcome", false), "asker", false)
	require.NoError(t, err)
	require.False(t, r.Payload.RequiresOutcome)
	for _, tc := range []fieldCase{{"answer", "attestation", nil, nil},
		{"answer", "evidence", nil, 1},
		{"request", "asker.kind", nil, "new"},
		{"request", "ticket.paths", nil, []string{"", "B"}},
		{"outcome", "receipt.at", nil, "2026-01-01T01:02:03+02:60"},
		{"request", "deadline", nil, "2026-01-01T1:02:03Z"}} {
		_, err := DecodeDecision(decisionMutation(t, tc.kind, tc.field, tc.bad), "asker", false)
		require.ErrorIs(t, err, ErrInvalidInput, tc.field)
	}
}

func TestDecisionAnswer_FreeTextAndOptionalInput(t *testing.T) {
	r := DecisionPayload{DecisionRequest: &DecisionRequest{}}
	a := DecisionPayload{DecisionAnswer: &DecisionAnswer{Note: "yes"}}
	require.NoError(t, ValidateDecisionAnswer(r, a, trimOnly))
	for _, bad := range []DecisionAnswer{{}, {Note: "x", InputValue: "unexpected"}, {Note: "x", Option: "yes"}} {
		a.DecisionAnswer = &bad
		require.ErrorIs(t, ValidateDecisionAnswer(r, a, trimOnly), ErrInvalidInput)
	}
	require.ErrorIs(t, ValidateDecisionAnswer(DecisionPayload{}, a, trimOnly), ErrInvalidInput)
	r.Options = []DecisionOption{{ID: "yes"}}
	a.DecisionAnswer = &DecisionAnswer{Option: "yes"}
	require.NoError(t, ValidateDecisionAnswer(r, a, trimOnly))
	a.InputValue = "extra"
	require.ErrorIs(t, ValidateDecisionAnswer(r, a, trimOnly), ErrInvalidInput)
	r.Options[0].Input = &DecisionInput{Type: "finding_id"}
	for _, v := range []string{"", strings.Repeat("f", 128), strings.Repeat("f", 129)} {
		a.InputValue = v
		err := ValidateDecisionAnswer(r, a, trimOnly)
		if len(v) <= 128 {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrInvalidInput)
		}
	}
}

func TestDecisionFields_MissingNestedMembers(t *testing.T) {
	for _, tc := range []fieldCase{
		{"request", "ticket", nil, map[string]any{}},
		{"request", "options", nil, []any{map[string]any{"label": "a"}}},
		{"request", "options.input", nil, map[string]any{"required": true}},
		{"answer", "attestation", nil, map[string]any{}},
		{"withdraw", "elsewhere", nil, map[string]any{}},
		{"outcome", "refusal", nil, map[string]any{}},
	} {
		_, err := DecodeDecision(decisionMutation(t, tc.kind, tc.field, tc.bad), "asker", false)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	for _, kind := range []string{"request", "answer", "outcome", "withdraw"} {
		d, err := DecodeDecision(decisionFixture(t, kind), "asker", false)
		require.NoError(t, err)
		raw, err := json.Marshal(d.Payload)
		require.NoError(t, err)
		_, err = DecodeDecision(raw, "asker", true)
		require.NoError(t, err, kind)
	}
}

func TestDecisionVariants_FieldApplicability(t *testing.T) {
	for _, tc := range []fieldCase{
		{"outcome", "refusal", nil, map[string]string{"text": "no"}},
		{"outcome", "seen_answers", nil, []string{}},
		{"refused", "receipt", nil, map[string]string{}},
		{"withdraw", "elsewhere", nil, map[string]any{"channel": "call", "decider": map[string]string{"kind": "user"}}},
	} {
		_, err := DecodeDecision(decisionMutation(t, tc.kind, tc.field, tc.bad), "asker", true)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	d, err := DecodeDecision(decisionMutation(t, "outcome", "receipt.seq", 1e21), "asker", true)
	require.NoError(t, err)
	require.EqualValues(t, 1e21, d.Payload.Receipt.Seq)
}

func TestDecisionFixtures_RequestAndAnswerAgree(t *testing.T) {
	request, err := DecodeDecision(decisionFixture(t, "request"), "asker", true)
	require.NoError(t, err)
	answer, err := DecodeDecision(decisionFixture(t, "answer"), "user", true)
	require.NoError(t, err)
	require.NoError(t, ValidateDecisionAnswer(request.Payload, answer.Payload, trimOnly))
	for _, name := range []string{"outcome", "refused"} {
		outcome, err := DecodeDecision(decisionFixture(t, name), "asker", true)
		require.NoError(t, err)
		require.Equal(t, answer.Payload.Target, outcome.Payload.Target)
		require.NotEqual(t, outcome.Payload.Target, outcome.Payload.Answer)
	}
}

func TestDecisionInput_LengthDependsOnOptionType(t *testing.T) {
	r, err := DecodeDecision(decisionFixture(t, "request"), "asker", true)
	require.NoError(t, err)
	for _, tc := range []struct {
		kind, value string
		valid       bool
	}{
		{"text", strings.Repeat("é", 8192), true},
		{"text", strings.Repeat("é", 8192) + " ", false},
		{"url", "https://example.com/" + strings.Repeat("a", 16384), true},
	} {
		a, err := DecodeDecision(decisionMutation(t, "answer", "input_value", tc.value), "user", true)
		require.NoError(t, err)
		r.Payload.Options[0].Input.Type = tc.kind
		err = ValidateDecisionAnswer(r.Payload, a.Payload, trimOnly)
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrInvalidInput)
		}
	}
}

func TestDecisionOpenedLinks_V1IndexRange(t *testing.T) {
	for _, tc := range []struct {
		index any
		valid bool
	}{{0, true}, {9, true}, {10, false}, {1.5, false}, {1e21, false}, {nil, false}} {
		_, err := DecodeDecision(decisionMutation(t, "answer", "opened_links", []any{tc.index}), "user", false)
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, ErrInvalidInput)
		}
	}
}

// trimOnly is the model tests normalizer: the model owns no Unicode tables.
func trimOnly(s string) (string, error) { return strings.TrimSpace(s), nil }

func TestDecisionAnswer_NormalizerFailureIsInvalidInput(t *testing.T) {
	r := DecisionPayload{DecisionRequest: &DecisionRequest{}}
	failing := func(string) (string, error) { return "", errors.New("cannot normalize") }
	for _, a := range []DecisionAnswer{{Note: "n"}, {InputValue: "i", Note: "n"}} {
		err := ValidateDecisionAnswer(r, DecisionPayload{DecisionAnswer: &a}, failing)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	// Only the second call fails: the note is normalized after the input.
	calls := 0
	second := func(s string) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("cannot normalize")
		}
		return s, nil
	}
	err := ValidateDecisionAnswer(r, DecisionPayload{DecisionAnswer: &DecisionAnswer{Note: "n"}}, second)
	require.ErrorIs(t, err, ErrInvalidInput)
}
