// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// adoption is what a restart learned about records the medium already
// holds past the cursor: the FR-21 §6.2 window between the append and
// the cursor advance. The journal rows they correspond to are verified
// by content, so they are neither republished nor skipped unseen.
type adoption struct {
	rows []sqlite.RelayJournalEvent
}

// verifier returns the writer's AdoptAhead hook. It accepts the medium's
// extra records only when they are, record for record, the journal's
// next events after the cursor. A disagreement is reported with
// segment.ErrAheadMismatch, which leaves the writer's
// RELAY_PEER_ID_CONFLICT refusal in force; a failure to read the journal
// is returned as itself, because it says nothing about a second writer.
func (a *adoption) verifier(ctx context.Context, j Journal, afterSeq int64) func([]segment.Record) error {
	return func(ahead []segment.Record) error {
		rows, err := j.ReadRelayJournalSince(ctx, afterSeq, len(ahead))
		if err != nil {
			return fmt.Errorf("read the journal to verify them: %w", err)
		}
		if len(rows) != len(ahead) {
			return fmt.Errorf("%w: the medium holds %d records past the cursor but the journal has %d events",
				segment.ErrAheadMismatch, len(ahead), len(rows))
		}
		for i, rec := range ahead {
			if err := sameEvent(rec, rows[i]); err != nil {
				return err
			}
		}
		a.rows = rows
		return nil
	}
}

// decodeExact decodes one JSON value keeping numbers as their literal
// text, so integers beyond 2^53 compare exactly instead of through a
// float64.
func decodeExact(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON value")
	}
	return v, nil
}

// sameEvent compares a medium record's payload with the journal event
// it should be, by decoded content so a change in field order between
// builds is not mistaken for a different event. The event id is
// rendered with format.EventID, never raw.
func sameEvent(rec segment.Record, row sqlite.RelayJournalEvent) error {
	id := format.EventID(row.Event.EventID)
	want, err := json.Marshal(row.Event)
	if err != nil {
		return fmt.Errorf("encode the journal event %s (journal seq %d): %w", id, row.Seq, err)
	}
	got, err := decodeExact(rec.Payload)
	if err != nil {
		return fmt.Errorf("%w: record at rs %d is not an event (expected journal seq %d, event %s): %w",
			segment.ErrAheadMismatch, rec.RS, row.Seq, id, err)
	}
	exp, err := decodeExact(want)
	if err != nil {
		return fmt.Errorf("decode the journal event %s (journal seq %d): %w", id, row.Seq, err)
	}
	if !reflect.DeepEqual(got, exp) {
		return fmt.Errorf("%w: record at rs %d differs from the journal event at that position (journal seq %d, event %s)",
			segment.ErrAheadMismatch, rec.RS, row.Seq, id)
	}
	return nil
}

// fastForward moves the cursor over the adopted records, which are
// already on the medium, and returns the journal rows still to publish.
func (p *Publisher) fastForward(ctx context.Context, a *adoption, rows []sqlite.RelayJournalEvent, w *segment.Writer) ([]sqlite.RelayJournalEvent, error) {
	if len(a.rows) == 0 {
		return rows, nil
	}
	last := a.rows[len(a.rows)-1]
	if err := p.cfg.Journal.AdvanceRelayPushCursor(ctx, last.Seq, w.NextRS()); err != nil {
		return nil, err
	}
	remaining := make([]sqlite.RelayJournalEvent, 0, len(rows))
	for _, row := range rows {
		if row.Seq > last.Seq {
			remaining = append(remaining, row)
		}
	}
	return remaining, nil
}
