// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
)

// staleJSONValue preserves object fields, including exact JSON numbers, and
// puts non-object responses under data (FR-6.2, MTIX-149). Fresh output retains
// its original shape. These response fields never enter persisted board data.
func staleJSONValue(value any, reason string) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal stale JSON output: %w", err)
	}
	var fields map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '{' {
		if decodeErr := json.Unmarshal(raw, &fields); decodeErr != nil {
			return nil, fmt.Errorf("decode stale JSON object: %w", decodeErr)
		}
	} else {
		fields = map[string]json.RawMessage{"data": raw}
	}
	reasonJSON, err := json.Marshal(reason)
	if err != nil {
		return nil, fmt.Errorf("encode stale reason: %w", err)
	}
	fields["stale"], fields["stale_reason"] = json.RawMessage("true"), reasonJSON
	return fields, nil
}
