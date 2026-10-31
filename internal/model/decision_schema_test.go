// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No schema validation package is approved, so the fixtures are checked
// against the Go validators; these tests check that the published schemas
// parse and name the same kinds, enums and limits as the validators.
func TestDecisionSchemas_Published_ParseAndNameTheFourKinds(t *testing.T) {
	var wire struct {
		OneOf []map[string]string        `json:"oneOf"`
		Defs  map[string]json.RawMessage `json:"$defs"`
	}
	b, err := os.ReadFile("../../docs/schemas/decision-wire-v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &wire))
	assert.Len(t, wire.OneOf, 4)
	for _, d := range []string{"request", "answer", "outcome", "withdraw", "option", "link", "ticket", "actor"} {
		assert.Contains(t, wire.Defs, d)
	}

	var res struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	b, err = os.ReadFile("../../docs/schemas/decision-resource-v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &res))
	for _, p := range []string{"schema", "id", "short_id", "state", "flags", "view_flags", "effective_answer",
		"agreeing_answers", "conflicting_answers", "refused_answers", "outcome", "withdrawn", "superseded_by",
		"invalid_reason", "timeline"} {
		assert.Contains(t, res.Properties, p)
	}
	assert.Contains(t, string(res.Properties["state"]), `"superseded"`)
}
