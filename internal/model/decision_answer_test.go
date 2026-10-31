// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func mustRequest(t *testing.T, mods ...func(obj) obj) *model.DecisionRequest {
	t.Helper()
	b := baseRequest()
	b["links"] = []any{obj{"url": "https://e.com/a"}, obj{"url": "https://e.com/b"}}
	b["options"] = []any{
		opt("plain"),
		obj{"id": "need-note", "note_required": true},
		obj{"id": "text", "input": obj{"type": "text", "required": true}},
		obj{"id": "finding", "input": obj{"type": "finding_id", "required": true}},
		obj{"id": "story", "input": obj{"type": "story_ref"}},
		obj{"id": "link", "input": obj{"type": "url", "required": true}},
	}
	for _, f := range mods {
		b = f(b)
	}
	res := ingest(t, b)
	require.Equal(t, model.DecisionValid, res.Class, res.Reason)
	return res.Value.Request
}

func human() model.DecisionActor { return model.DecisionActor{Kind: "human"} }

func TestValidateDecisionAnswer_AgainstRequest_Table(t *testing.T) {
	tests := []struct {
		name    string
		ans     model.DecisionAnswer
		wantErr bool
	}{
		{"plain option", model.DecisionAnswer{Option: "plain", Decider: human()}, false},
		{"unknown option", model.DecisionAnswer{Option: "nope", Decider: human()}, true},
		{"missing option", model.DecisionAnswer{Decider: human()}, true},
		{"note required and empty", model.DecisionAnswer{Option: "need-note", Decider: human()}, true},
		{"note required and whitespace", model.DecisionAnswer{Option: "need-note", Note: " \t\n ", Decider: human()}, true},
		{"note required and given", model.DecisionAnswer{Option: "need-note", Note: " why ", Decider: human()}, false},
		{"text input required and missing", model.DecisionAnswer{Option: "text", Decider: human()}, true},
		{"text input whitespace", model.DecisionAnswer{Option: "text", InputValue: "   ", Decider: human()}, true},
		{"text input given", model.DecisionAnswer{Option: "text", InputValue: "go on", Decider: human()}, false},
		{"text input at cap", model.DecisionAnswer{Option: "text", InputValue: rep("x", 16*1024), Decider: human()}, false},
		{"text input over cap", model.DecisionAnswer{Option: "text", InputValue: rep("x", 16*1024+1), Decider: human()}, true},
		{"finding id ok", model.DecisionAnswer{Option: "finding", InputValue: "F-1.2:a#3/b", Decider: human()}, false},
		{"finding id bad grammar", model.DecisionAnswer{Option: "finding", InputValue: "has space", Decider: human()}, true},
		{"finding id over cap", model.DecisionAnswer{Option: "finding", InputValue: rep("f", 129), Decider: human()}, true},
		{"story ref optional and empty", model.DecisionAnswer{Option: "story", Decider: human()}, false},
		{"story ref optional and bad", model.DecisionAnswer{Option: "story", InputValue: "a b", Decider: human()}, true},
		{"url input ok", model.DecisionAnswer{Option: "link", InputValue: "https://example.com/x", Decider: human()}, false},
		{"url input javascript", model.DecisionAnswer{Option: "link", InputValue: "javascript:alert(1)", Decider: human()}, true},
		{"input given for an option without input", model.DecisionAnswer{Option: "plain", InputValue: "x", Decider: human()}, true},
		{"agent decider on a human request", model.DecisionAnswer{Option: "plain", Decider: model.DecisionActor{Kind: "agent"}}, true},
		{"opened link in range", model.DecisionAnswer{Option: "plain", Decider: human(), OpenedLinks: []int{0, 1}}, false},
		{"opened link out of range", model.DecisionAnswer{Option: "plain", Decider: human(), OpenedLinks: []int{2}}, true},
		{"opened link negative", model.DecisionAnswer{Option: "plain", Decider: human(), OpenedLinks: []int{-1}}, true},
	}
	req := mustRequest(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateDecisionAnswer(req, &tt.ans)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateDecisionAnswer_AnswerableByAny_AcceptsAgent(t *testing.T) {
	req := mustRequest(t, func(o obj) obj { return withField(o, "answerable_by", "any") })
	agent := model.DecisionAnswer{Option: "plain", Decider: model.DecisionActor{Kind: "agent"}}
	require.NoError(t, model.ValidateDecisionAnswer(req, &agent))
}

func TestValidateDecisionAnswer_RequestWithoutOptions_NeedsNote(t *testing.T) {
	req := mustRequest(t, func(o obj) obj { return withField(o, "options", nil) })
	tests := []struct {
		name    string
		ans     model.DecisionAnswer
		wantErr bool
	}{
		{"note", model.DecisionAnswer{Note: "do it", Decider: human()}, false},
		{"no note", model.DecisionAnswer{Decider: human()}, true},
		{"whitespace note", model.DecisionAnswer{Note: "\u00a0 \n", Decider: human()}, true},
		{"option named", model.DecisionAnswer{Option: "x", Note: "n", Decider: human()}, true},
		{"input given", model.DecisionAnswer{InputValue: "x", Note: "n", Decider: human()}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateDecisionAnswer(req, &tt.ans)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestValidateDecisionAnswer_NilArguments_Refused(t *testing.T) {
	require.ErrorIs(t, model.ValidateDecisionAnswer(nil, &model.DecisionAnswer{}), model.ErrInvalidInput)
	require.ErrorIs(t, model.ValidateDecisionAnswer(&model.DecisionRequest{}, nil), model.ErrInvalidInput)
}

func TestNormalizeDecisionText_Rules_Table(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"trim spaces", "  a b  ", "a b"},
		{"trim tabs and newlines", "\t\na\r\n", "a"},
		{"trim no-break space", "\u00a0a\u00a0", "a"},
		{"inner whitespace counts", "a  b", "a  b"},
		{"case counts", "Approve", "Approve"},
		{"whitespace only", " \u2003\n ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.NormalizeDecisionText(tt.in))
		})
	}
}

// TestNormalizeDecisionText_NFC_IsADocumentedGap records the missing step.
// Unicode NFC needs golang.org/x/text/unicode/norm, which is not an approved
// package. The two spellings of "é" below must agree once it is approved.
func TestNormalizeDecisionText_NFC_IsADocumentedGap(t *testing.T) {
	t.Skip("blocked: NFC needs approval of golang.org/x/text; compose=\"\\u00e9\" and decompose=\"e\\u0301\" must then normalise equal")
}

func TestDecisionAnswerValue_StrictTriple(t *testing.T) {
	a := &model.DecisionAnswer{Option: "x", InputValue: " v ", Note: "n\n"}
	b := &model.DecisionAnswer{Option: "x", InputValue: "v", Note: " n"}
	assert.Equal(t, a.Value(), b.Value())
	for _, other := range []*model.DecisionAnswer{
		{Option: "X", InputValue: "v", Note: "n"},
		{Option: "x", InputValue: "V", Note: "n"},
		{Option: "x", InputValue: "v", Note: "N"},
		{Option: "x", InputValue: "v", Note: "n n"},
		{Option: "x", InputValue: "v"},
	} {
		assert.NotEqual(t, a.Value(), other.Value())
	}
	assert.Equal(t, (&model.DecisionAnswer{}).Value(), (&model.DecisionAnswer{Note: "  "}).Value())
}

func TestDecisionShortID_Derivation_Table(t *testing.T) {
	for _, id := range []string{"", "evt-1", "01J0000000000000000000000A", rep("x", 4096), "é"} {
		sum := sha256.Sum256([]byte(id))
		want := "d-" + hex.EncodeToString(sum[:])[:8]
		assert.Equal(t, want, model.DecisionShortID(id))
		assert.Len(t, model.DecisionShortID(id), 10)
	}
	assert.Equal(t, "d-e3b0c442", model.DecisionShortID(""))
	assert.NotEqual(t, model.DecisionShortID("a"), model.DecisionShortID("b"))
}

func TestValidateDecisionLinkURL_Table(t *testing.T) {
	ok := []string{
		"https://example.com", "https://example.com:8443/a?b=c#d", "http://127.0.0.1", "http://127.0.0.1:1/x",
		"http://[::1]/", "http://[::1]:9/", "http://localhost", "http://LOCALHOST:3000/x",
	}
	bad := []string{
		"", "http://example.com", "http://127.0.0.2/", "http://0.0.0.0/", "http://localhost.example.com/",
		"ftp://example.com/", "javascript:alert(1)", "JaVaScRiPt:alert(1)", "data:text/plain,x", "file:///etc/hosts",
		"https://user@example.com/", "https://user:pw@example.com/", "http://localhost@evil.com/", "https://",
		"//example.com/", "/relative", "https://exa mple.com/", "https://example.com/\n", "mailto:a@b.c",
		"https://" + rep("a", 2050),
	}
	for _, u := range ok {
		assert.NoError(t, model.ValidateDecisionLinkURL(u), u)
	}
	for _, u := range bad {
		err := model.ValidateDecisionLinkURL(u)
		require.Error(t, err, u)
		assert.ErrorIs(t, err, model.ErrInvalidInput)
	}
}
