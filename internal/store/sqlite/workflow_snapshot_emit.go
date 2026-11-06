// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// All workflow emitters pass here, including raw callers and future callers.
// Capture after the mutation, before event clocks, inside that same transaction.
// Legacy-winner repair is the sole compatibility exemption.
func captureWorkflowSnapshot(ctx context.Context, tx *sql.Tx, p emitParams) (emitParams, error) {
	if !isWorkflowOp(p.OpType) || p.LegacyWorkflowPayload {
		return p, nil
	}
	fields := map[string]json.RawMessage{}
	if len(p.Payload) > 0 {
		if err := json.Unmarshal(p.Payload, &fields); err != nil {
			return p, fmt.Errorf("decode emitted workflow payload: %w", err)
		}
	}
	at, err := readClosedAtSnapshot(ctx, tx, p.NodeID)
	if err != nil {
		return p, err
	}
	if value, exists := fields["closed_at"]; exists {
		if bytes.Equal(bytes.TrimSpace(value), at) {
			return p, nil
		}
		fields["closed_at"] = at
		p.Payload, err = json.Marshal(fields)
		if err != nil {
			return p, fmt.Errorf("encode workflow snapshot: %w", err)
		}
		return p, nil
	}
	// Append without reordering the existing typed payload or dropping unknown
	// keys. The object was decoded above; null/empty becomes a new object.
	raw := bytes.TrimSpace(p.Payload)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	out := append([]byte(nil), raw[:len(raw)-1]...)
	if len(fields) > 0 {
		out = append(out, ',')
	}
	out = append(out, `"closed_at":`...)
	out = append(out, at...)
	out = append(out, '}')
	p.Payload = out
	return p, nil
}

func prepareEmittedPayload(ctx context.Context, tx *sql.Tx, p emitParams) (emitParams, error) {
	if err := validateEmittedAnnotation(p); err != nil {
		return p, err
	}
	return captureWorkflowSnapshot(ctx, tx, p)
}
