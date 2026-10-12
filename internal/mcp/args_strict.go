// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// decodeStrictArgs decodes tool arguments into dst and rejects unknown keys,
// wrong types and malformed JSON with ErrInvalidInput, so a tool never runs
// with part of the caller's request silently dropped (MTIX-146). Absent
// arguments (nil or empty) leave dst at its zero value.
func decodeStrictArgs(args json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid tool arguments: %v: %w", err, model.ErrInvalidInput)
	}
	return nil
}
