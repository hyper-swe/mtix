// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

func TestDecisionLimits_MirrorTheSyncValidator(t *testing.T) {
	assert.Equal(t, validator.MaxPayloadBytes, model.MaxDecisionCommentPayloadBytes)
	assert.Equal(t, validator.MaxPayloadNestingDepth, model.MaxDecisionNestingDepth)
}

// shuffledObject writes m as JSON with the members in a random order, and
// with every member written twice (an earlier, different value first) when
// dupes is set, at every nesting level.
func shuffledObject(rng *rand.Rand, v any, dupes bool) string {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		var parts []string
		for _, k := range keys {
			kb, _ := json.Marshal(k)
			if dupes {
				parts = append(parts, string(kb)+`:{"stale":["old",1]}`)
			}
			parts = append(parts, string(kb)+":"+shuffledObject(rng, t[k], dupes))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = shuffledObject(rng, e, dupes)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestIngestDecision_AnyMemberOrderWithOrWithoutDuplicates_SameHash(t *testing.T) {
	const seed = 219004
	rng := rand.New(rand.NewPCG(seed, seed))
	base := baseRequest()
	base["links"] = []any{obj{"url": "https://e.com/a", "label": "A"}}
	base["source"] = obj{"app": "x", "run": "r"}
	base["answerers"] = []any{"alice"}
	want := ingest(t, base)
	require.Equal(t, model.DecisionValid, want.Class, want.Reason)
	for i := range 200 {
		dupes := i%2 == 0
		raw := shuffledObject(rng, any(map[string]any(base)), dupes)
		got := model.IngestDecision([]byte(raw))
		require.Equal(t, model.DecisionValid, got.Class, "seed %d iteration %d: %s", seed, i, got.Reason)
		assert.Equal(t, want.PayloadSHA256, got.PayloadSHA256, "seed %d iteration %d", seed, i)
		assert.Equal(t, want.ContentSHA256, got.ContentSHA256, "seed %d iteration %d", seed, i)
		assert.Equal(t, want.Value, got.Value, "seed %d iteration %d", seed, i)
		assert.Equal(t, dupes, got.HadDuplicates, "seed %d iteration %d", seed, i)
	}
}

func TestIngestDecision_ContentHash_IgnoresRequestKeyAndSupersedes(t *testing.T) {
	a := ingest(t, withField(withField(baseRequest(), "request_key", "k1"), "supersedes", []any{"e1"}))
	b := ingest(t, withField(withField(baseRequest(), "request_key", "k2"), "supersedes", []any{"e2", "e3"}))
	c := ingest(t, withField(baseRequest(), "question", "Another question?"))
	assert.NotEqual(t, a.PayloadSHA256, b.PayloadSHA256)
	assert.Equal(t, a.ContentSHA256, b.ContentSHA256)
	assert.NotEqual(t, a.ContentSHA256, c.ContentSHA256)
	// A request without either field hashes like one that has them removed.
	d := ingest(t, baseRequest())
	e := ingest(t, withField(baseRequest(), "supersedes", []any{}))
	assert.Equal(t, d.ContentSHA256, e.ContentSHA256)
}

func TestIngestDecision_PayloadHash_CoversUnknownFields(t *testing.T) {
	plain := ingest(t, baseRequest())
	for _, extra := range []any{1, "x", []any{1}, obj{"deep": obj{"er": true}}, nil} {
		withExtra := ingest(t, withField(baseRequest(), "novel", extra))
		if extra == nil {
			// withField drops nil, so the object is unchanged.
			assert.Equal(t, plain.PayloadSHA256, withExtra.PayloadSHA256)
			continue
		}
		assert.NotEqual(t, plain.PayloadSHA256, withExtra.PayloadSHA256, "%v", extra)
	}
}

func TestIngestDecision_NumbersEquivalentSpellings_SameHash(t *testing.T) {
	spellings := []string{`1`, `1.0`, `1e0`, `10e-1`, `0.1e1`, `1.00E+0`}
	var first string
	for _, n := range spellings {
		raw := `{"v":` + n + `,"kind":"withdraw","request_key":"w","target":"t"}`
		res := model.IngestDecision([]byte(raw))
		require.Equal(t, model.DecisionValid, res.Class, "%s: %s", n, res.Reason)
		if first == "" {
			first = res.PayloadSHA256
		}
		assert.Equal(t, first, res.PayloadSHA256, n)
	}
}

func TestValidateDecisionForWrite_Table(t *testing.T) {
	valid := string(mustJSON(t, baseRequest()))
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"valid", valid, true},
		{"duplicate member", `{"v":1,"v":1,` + valid[1:], false},
		{"nested duplicate", strings.Replace(valid, `"uid":"u1"`, `"uid":"u1","uid":"u2"`, 1), false},
		{"number overflow", strings.Replace(valid, `"v":1`, `"v":1,"n":1e999`, 1), false},
		{"unknown field is fine", strings.Replace(valid, `"v":1`, `"v":1,"extra":true`, 1), true},
		{"future version", strings.Replace(valid, `"v":1`, `"v":2`, 1), false},
		{"malformed", `{"v":1,"kind":"request"}`, false},
		{"not JSON", `nope`, false},
		{"oversize", `{"x":"` + rep("a", 65*1024) + `"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateDecisionForWrite([]byte(tt.raw))
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func commentWith(t *testing.T, decision any) []byte {
	t.Helper()
	return mustJSON(t, obj{"author_id": "agent-1", "to": "alice", "hop": 0, "body": "DECISION NEEDED", "decision": decision})
}

func TestIngestDecisionFromComment_Table(t *testing.T) {
	t.Run("finds the decision", func(t *testing.T) {
		res, ok := model.IngestDecisionFromComment(commentWith(t, baseRequest()))
		require.True(t, ok)
		assert.Equal(t, model.DecisionValid, res.Class, res.Reason)
	})
	t.Run("malformed decision stays a classification", func(t *testing.T) {
		res, ok := model.IngestDecisionFromComment(commentWith(t, "not an object"))
		require.True(t, ok)
		assert.Equal(t, model.DecisionMalformed, res.Class)
	})
	t.Run("duplicate decision members: last wins", func(t *testing.T) {
		raw := `{"author_id":"a","decision":{"v":1},"decision":` + string(mustJSON(t, baseRequest())) + `}`
		res, ok := model.IngestDecisionFromComment([]byte(raw))
		require.True(t, ok)
		assert.Equal(t, model.DecisionValid, res.Class, res.Reason)
	})
	for name, raw := range map[string]string{
		"ordinary comment": `{"author_id":"a","body":"hi"}`,
		"null decision":    `{"author_id":"a","decision":null}`,
		"not JSON":         `{`,
		"too deep":         `{"decision":` + rep(`{"a":`, 10) + `1` + rep(`}`, 10) + `}`,
		"oversize":         `{"body":"` + rep("a", 65*1024) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := model.IngestDecisionFromComment([]byte(raw))
			assert.False(t, ok)
		})
	}
}

func TestValidateDecisionCommentForWrite_Table(t *testing.T) {
	good := commentWith(t, baseRequest())
	require.NoError(t, model.ValidateDecisionCommentForWrite(good))
	require.NoError(t, model.ValidateDecisionCommentForWrite([]byte(`{"author_id":"a","body":"hi"}`)))
	for name, raw := range map[string]string{
		"duplicate outer member": `{"author_id":"a","author_id":"b","body":"x"}`,
		"malformed decision":     string(commentWith(t, obj{"v": 1})),
		"oversize":               `{"body":"` + rep("a", 65*1024) + `"}`,
		"too deep":               `{"a":` + rep(`[`, 10) + rep(`]`, 10) + `}`,
		"not JSON":               `[`,
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, model.ValidateDecisionCommentForWrite([]byte(raw)), model.ErrInvalidInput)
		})
	}
}

func TestIngestDecision_WorstCaseRequest_FitsPayloadCapAndDepth(t *testing.T) {
	r := baseRequest()
	r["context"] = rep("c", 32*1024)
	opts := make([]any, 9)
	for i := range opts {
		opts[i] = obj{"id": "o" + string(rune('a'+i)), "label": rep("l", 80), "consequence": rep("c", 160),
			"input": obj{"type": "text", "required": true}}
	}
	r["options"] = opts
	payload := commentWith(t, r)
	require.Less(t, len(payload), model.MaxDecisionCommentPayloadBytes)
	require.NoError(t, model.ValidateDecisionCommentForWrite(payload))
	r["context"] = rep("c", 32*1024+1)
	assert.Equal(t, model.DecisionMalformed, ingest(t, r).Class)
}

func TestDecisionKinds_ListsTheFour(t *testing.T) {
	assert.ElementsMatch(t, []model.DecisionKind{"request", "answer", "outcome", "withdraw"}, model.DecisionKinds())
}
