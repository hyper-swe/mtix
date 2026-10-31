// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// A decision that does not validate still names the request it points at,
// so a reader can show it under that request.
func TestIngestDecision_TargetAndRequestKey_ReadEvenWhenNotValid(t *testing.T) {
	tests := []struct {
		name       string
		obj        obj
		wantClass  model.DecisionClass
		wantTarget string
		wantKey    string
	}{
		{"valid answer", baseAnswer(), model.DecisionValid, "evt-1", "a1"},
		{"malformed answer keeps its target", withField(baseAnswer(), "decider", nil), model.DecisionMalformed, "evt-1", "a1"},
		{"future version keeps its target", withField(baseAnswer(), "v", 2), model.DecisionUnsupported, "evt-1", "a1"},
		{"unknown kind keeps its target", withField(baseAnswer(), "kind", "vote"), model.DecisionUnsupported, "evt-1", "a1"},
		{"target with bad grammar is dropped", withField(baseAnswer(), "target", "a b"), model.DecisionMalformed, "", "a1"},
		{"target of wrong type is dropped", withField(baseAnswer(), "target", 5), model.DecisionMalformed, "", "a1"},
		{"request key with bad grammar is dropped", withField(baseAnswer(), "request_key", "a b"), model.DecisionMalformed, "evt-1", ""},
		{"request has no target", baseRequest(), model.DecisionValid, "", "6f1c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ingest(t, tt.obj)
			assert.Equal(t, tt.wantClass, res.Class, res.Reason)
			assert.Equal(t, tt.wantTarget, res.Target)
			assert.Equal(t, tt.wantKey, res.RequestKey)
		})
	}
	t.Run("unparseable bytes name nothing", func(t *testing.T) {
		res := model.IngestDecision([]byte(`{"target":`))
		assert.Empty(t, res.Target)
		assert.Empty(t, res.RequestKey)
	})
}
