// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// QuarantineAcknowledgementPageSize bounds each receipt lookup's JSON array
// and matches the service's quarantine page size (MTIX-187.2.7).
const QuarantineAcknowledgementPageSize = 500

type quarantineReceiptLookup struct {
	EventIDHex  string `json:"event_id_hex"`
	RawEventHex string `json:"raw_event_hex"`
}

// Match the exact page snapshot in one bound JSON parameter. Hex transport
// preserves arbitrary stored bytes; decode before comparison so the receipt
// primary key (event_id, raw_event) remains usable. No live quarantine reread
// can attach a receipt for different raw bytes under a reused event id.
const quarantineAcknowledgementsQuery = `SELECT a.event_id, a.raw_event_sha256,
 a.source, a.actor, a.reason, a.acknowledged_at
 FROM json_each(?) AS lookup
 JOIN sync_quarantine_acknowledgements AS a
 ON a.event_id = CAST(unhex(json_extract(lookup.value, '$.event_id_hex')) AS TEXT)
 AND a.raw_event = CAST(unhex(json_extract(lookup.value, '$.raw_event_hex')) AS TEXT)`

// QuarantineAcknowledgementsFor reads exact receipts for one bounded page of
// retained pull/sweep snapshots. It preserves legacy readonly stores without
// additive audit metadata. Pages larger than QuarantineAcknowledgementPageSize
// return model.ErrInvalidInput (MTIX-187.2.7).
func (s *Store) QuarantineAcknowledgementsFor(ctx context.Context, page []QuarantinedEvent) (out map[string]QuarantineAcknowledgement, err error) {
	out = make(map[string]QuarantineAcknowledgement)
	if len(page) > QuarantineAcknowledgementPageSize {
		return nil, fmt.Errorf("quarantine receipt page exceeds %d rows: %w", QuarantineAcknowledgementPageSize, model.ErrInvalidInput)
	}
	keys := quarantineReceiptLookups(page)
	// Empty and push-only pages need no receipt lookup. Avoid inspecting audit
	// metadata so unrelated read-only schema errors cannot fail these pages.
	if len(keys) == 0 {
		return out, nil
	}
	available, err := s.quarantineAcknowledgementsAvailable(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect quarantine acknowledgements: %w", err)
	}
	if !available {
		return out, nil
	}
	payload, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("encode quarantine receipt keys: %w", err)
	}
	rows, err := s.Query(ctx, quarantineAcknowledgementsQuery, string(payload))
	if err != nil {
		return nil, fmt.Errorf("read quarantine acknowledgements: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close quarantine acknowledgements: %w", closeErr))
		}
	}()
	for rows.Next() {
		var a QuarantineAcknowledgement
		if err = rows.Scan(&a.EventID, &a.RawEventSHA256, &a.Source, &a.Actor, &a.Reason, &a.AcknowledgedAt); err != nil {
			return nil, fmt.Errorf("scan quarantine acknowledgement: %w", err)
		}
		out[a.EventID] = a
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read quarantine acknowledgements: %w", err)
	}
	return out, nil
}

func quarantineReceiptLookups(page []QuarantinedEvent) []quarantineReceiptLookup {
	keys := make([]quarantineReceiptLookup, 0, len(page))
	for _, q := range page {
		if q.Source == "pull" || q.Source == "sweep" {
			keys = append(keys, quarantineReceiptLookup{
				EventIDHex:  hex.EncodeToString([]byte(q.EventID)),
				RawEventHex: hex.EncodeToString([]byte(q.RawEvent)),
			})
		}
	}
	return keys
}
