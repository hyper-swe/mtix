// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator

import "encoding/json"

// LargestPayloadField names the field that makes up most of an event
// payload, so a payload over MaxPayloadBytes can be reported by the field
// the user must shorten or split (MTIX-95.12). An update_field payload
// names its field in field_name; for any other object payload it is the
// key with the longest encoded value (ties go to the key that sorts first),
// with the payload keys of set_prompt, set_acceptance and comment events
// reported as prompt, acceptance and comment. A payload that is not a JSON
// object, or is empty, is reported as "payload".
func LargestPayloadField(payload json.RawMessage) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || len(fields) == 0 {
		return "payload"
	}
	if raw, ok := fields["field_name"]; ok {
		var name string
		if json.Unmarshal(raw, &name) == nil && name != "" {
			return name
		}
	}
	best, bestLen := "", -1
	for key, value := range fields {
		if len(value) > bestLen || (len(value) == bestLen && key < best) {
			best, bestLen = key, len(value)
		}
	}
	return userFieldName(best)
}

// userFieldName maps a payload key to the node field a user edits through
// it, where the two differ.
func userFieldName(key string) string {
	switch key {
	case "prompt_text":
		return "prompt"
	case "acceptance_text":
		return "acceptance"
	case "body":
		return "comment"
	}
	return key
}
