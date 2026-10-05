// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// payloadEncoder is instance-scoped so failures can be exercised without
// process-global state. Install it before concurrent use of the Store.
type payloadEncoder func(any) (json.RawMessage, error)

func (s *Store) encodePayload(value any) (json.RawMessage, error) {
	if s.encodePayloadFn != nil {
		return s.encodePayloadFn(value)
	}
	return model.EncodePayload(value)
}

// emitPayload keeps encoding failures inside the caller's FR-18.3 transaction.
// Preserve the cause and identify the operation and node before any event or
// clock is written; WithTx rolls back earlier mutations and suppresses hooks.
func (s *Store) emitPayload(ctx context.Context, tx *sql.Tx, p emitParams, value any) error {
	payload, err := s.encodePayload(value)
	if err != nil {
		return fmt.Errorf("encode %s payload for %s: %w", p.OpType, p.NodeID, err)
	}
	p.Payload = payload
	return emitEvent(ctx, tx, p)
}
