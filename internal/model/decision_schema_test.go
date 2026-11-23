// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The Go tests cannot run a JSON Schema validator (none is an approved
// package), so this pins the parts of the card contract the negative fixtures
// rely on. Full validation of the fixtures against the schemas needs an external
// JSON Schema 2020-12 validator and is not part of this test.
func TestDecisionResourceSchema_RequiresCardFields(t *testing.T) {
	var schema struct {
		Required []string `json:"required"`
		AllOf    []struct {
			Then struct {
				Required []string `json:"required"`
			} `json:"then"`
		} `json:"allOf"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	b, err := os.ReadFile("../../docs/schemas/decision-resource-v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &schema))
	required := map[string]bool{}
	for _, name := range schema.Required {
		required[name] = true
	}
	for _, name := range []string{"schema", "id", "short_id", "state", "flags", "view_flags", "node_id", "asker", "to", "raised_at", "now", "timeline"} {
		require.True(t, required[name], "unconditional field %s must be required", name)
	}
	conditional := map[string]bool{}
	for _, branch := range schema.AllOf {
		for _, name := range branch.Then.Required {
			conditional[name] = true
		}
	}
	for _, name := range []string{"question", "answerable_by", "requires_outcome", "effective_answer", "outcome", "withdrawn", "superseded_by", "invalid_reason"} {
		require.True(t, conditional[name], "state-specific field %s must be required by a branch", name)
	}
	require.Contains(t, string(schema.Properties["raised_at"]), `"null"`, "an unknown age is a null raised_at")

	negatives, err := filepath.Glob("../../docs/schemas/fixtures/resource-invalid-missing-*.json")
	require.NoError(t, err)
	require.Len(t, negatives, 4)
	for _, file := range negatives {
		member := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(file), "resource-invalid-missing-"), ".json"), "-", "_")
		require.True(t, required[member] || conditional[member], "%s is not required by the schema", member)
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		var card map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &card))
		require.NotContains(t, card, member, file)
		require.Equal(t, "open", strings.Trim(string(card["state"]), `"`))
	}
}
