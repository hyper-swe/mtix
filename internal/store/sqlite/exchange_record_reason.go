// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local receipt diagnostics retain write safety without scheduling board exports.
package sqlite

import (
	"context"
	"fmt"
)

// SaveExchangeRecordReason persists only the local receipt diagnostic key.
// A single statement is atomic. Like NextSequence, it retains NFR-2.8 guards
// outside WithTx; this non-exported bookkeeping does not notify board callbacks.
// The service supplies a normalized fixed diagnostic category or clears it.
func (s *Store) SaveExchangeRecordReason(ctx context.Context, reason string) error {
	if err := s.preflightWrite(); err != nil {
		return err
	}
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES ('meta.sync.exchange_record.reason',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, reason)
	if err != nil {
		return s.classifyWriteError(wrapBusyError(fmt.Errorf("save exchange record reason: %w", err)))
	}
	return nil
}
