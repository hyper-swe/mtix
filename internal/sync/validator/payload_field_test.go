// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// TestLargestPayloadField_Payloads_NameTheField: the field reported for an
// event payload is the one the user edits (MTIX-95.12): update_field names
// its field, other payloads report their largest key, with the set_prompt,
// set_acceptance and comment keys named prompt, acceptance and comment.
func TestLargestPayloadField_Payloads_NameTheField(t *testing.T) {
	big := strings.Repeat("x", 1000)
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"create_node with the prompt largest", `{"title":"t","description":"d","prompt":"` + big + `"}`, "prompt"},
		{"create_node with the description largest", `{"title":"t","description":"` + big + `","prompt":"p"}`, "description"},
		{"set_prompt", `{"prompt_text":"` + big + `"}`, "prompt"},
		{"set_acceptance", `{"acceptance_text":"` + big + `"}`, "acceptance"},
		{"comment", `{"author_id":"a","body":"` + big + `"}`, "comment"},
		{"update_field names its field", `{"field_name":"title","new_value":"` + big + `"}`, "title"},
		{"update_field with an empty field_name", `{"field_name":"","new_value":"` + big + `"}`, "new_value"},
		{"tie goes to the key that sorts first", `{"b":"12","a":"34"}`, "a"},
		{"not an object", `"` + big + `"`, "payload"},
		{"empty object", `{}`, "payload"},
		{"not JSON", `{`, "payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, validator.LargestPayloadField(json.RawMessage(tt.payload)))
		})
	}
}
