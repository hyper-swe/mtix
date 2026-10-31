// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type obj = map[string]any

func baseRequest() obj {
	return obj{
		"v": 1, "kind": "request", "request_key": "6f1c",
		"question": "Merge the run into main?",
		"options": []any{
			obj{"id": "approve", "label": "approve", "consequence": "Merge."},
			obj{"id": "directive", "label": "directive", "input": obj{"type": "text", "required": true}},
		},
		"ticket": obj{"uid": "u1", "ancestors": []any{"u0"},
			"paths": []any{"P-1.4", "P-1"}, "path_match": []any{true, true}, "truncated": false},
	}
}

func baseAnswer() obj {
	return obj{
		"v": 1, "kind": "answer", "request_key": "a1", "target": "evt-1", "option": "approve",
		"decider": obj{"kind": "human"}, "channel": "web",
	}
}

func baseOutcome() obj {
	return obj{"v": 1, "kind": "outcome", "request_key": "o1", "target": "evt-1", "answer": "evt-2", "result": "applied"}
}

func baseWithdraw() obj {
	return obj{"v": 1, "kind": "withdraw", "request_key": "w1", "target": "evt-1"}
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func rep(s string, n int) string { return strings.Repeat(s, n) }

// withField returns a copy of base with key set to v (a nil v deletes it).
func withField(base obj, key string, v any) obj {
	out := obj{}
	for k, e := range base {
		out[k] = e
	}
	if v == nil {
		delete(out, key)
	} else {
		out[key] = v
	}
	return out
}

func ingest(t testing.TB, v any) model.DecisionIngest {
	t.Helper()
	return model.IngestDecision(mustJSON(t, v))
}
