// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// foldedVariants returns near-miss spellings of id that an exact-byte lookup
// must not match: a case change, LIKE wildcards and a trim (MTIX-187.7.17).
func foldedVariants(id string, stored map[string]bool) []string {
	candidates := []string{
		strings.ToUpper(id), strings.ToLower(id), strings.TrimSpace(id),
		"%", id + "%", "%" + id, "_" + id, id + "_",
	}
	var out []string
	for _, v := range candidates {
		if !stored[v] {
			out = append(out, v)
		}
	}
	return out
}

// probeStore holds every hostile id in the four tables the exact-id lookups
// read: applied_events, the quarantine, its acknowledgements and pending
// sync_events. It returns the stored ids and the raw event of each.
func probeStore(t *testing.T) (*sqlite.Store, map[string]bool, map[string]string) {
	t.Helper()
	s, raw := hostileStore(t)
	stored := map[string]bool{}
	rawEvents := map[string]string{}
	node := int64(0)
	for _, fx := range testutil.HostileIDs() {
		if len(fx.Value) > hostileWireLimit {
			continue
		}
		node++
		require.NoError(t, hostileApply(t, s, hostileCreate(t, fx.Value, "MTIX-"+hostileItoa(node), node)))
		_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pending' WHERE event_id = ?`, fx.Value)
		require.NoError(t, err)
		row := hostileQuarantineRow(t, fx.Value)
		hostileQuarantine(t, s, row)
		_, err = s.RecordQuarantineAcknowledgement(context.Background(), row, sqlite.QuarantineAcknowledgement{
			EventID: fx.Value, RawEventSHA256: sha256Hex(row.RawEvent), Source: row.Source, Actor: "op",
			Reason: "reviewed", AcknowledgedAt: "2026-03-14T11:00:00Z",
		})
		require.NoError(t, err)
		stored[fx.Value] = true
		rawEvents[fx.Value] = row.RawEvent
	}
	require.NotEmpty(t, stored)
	return s, stored, rawEvents
}

// TestSyncEventID_FoldedVariantsReadAbsent stores every hostile id, then
// asks each exact-id lookup about each folded variant of every id: none may
// answer present, so LIKE, lower() and trim mutants in those lookups fail.
func TestSyncEventID_FoldedVariantsReadAbsent(t *testing.T) {
	ctx := context.Background()
	s, stored, rawEvents := probeStore(t)
	probes := 0
	for id := range stored {
		for _, v := range foldedVariants(id, stored) {
			probes++
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
				applied, err := sqlite.EventApplied(ctx, tx, v)
				require.NoError(t, err)
				require.False(t, applied, "EventApplied matched %q for %q", v, id)
				quarantined, err := sqlite.IsQuarantined(ctx, tx, v)
				require.NoError(t, err)
				require.False(t, quarantined, "IsQuarantined matched %q for %q", v, id)
				return nil
			}))
			q := hostileQuarantineRow(t, v)
			q.RawEvent = rawEvents[id]
			receipt, err := s.QuarantineAcknowledgementFor(ctx, q)
			require.NoError(t, err)
			require.Nil(t, receipt, "QuarantineAcknowledgementFor matched %q for %q", v, id)
			pending, err := s.ReadPendingEventsByID(ctx, []string{v})
			require.NoError(t, err)
			require.Empty(t, pending, "ReadPendingEventsByID matched %q for %q", v, id)
		}
	}
	require.Positive(t, probes)
}

// TestSyncEventID_ExactIDsReadPresent is the positive control: the same four
// lookups find every stored id by its exact bytes, so the absent probes above
// cannot pass only because the setup stored nothing.
func TestSyncEventID_ExactIDsReadPresent(t *testing.T) {
	ctx := context.Background()
	s, stored, rawEvents := probeStore(t)
	for id := range stored {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			applied, err := sqlite.EventApplied(ctx, tx, id)
			require.NoError(t, err)
			require.True(t, applied, "EventApplied lost %q", id)
			quarantined, err := sqlite.IsQuarantined(ctx, tx, id)
			require.NoError(t, err)
			require.True(t, quarantined, "IsQuarantined lost %q", id)
			return nil
		}))
		q := hostileQuarantineRow(t, id)
		q.RawEvent = rawEvents[id]
		receipt, err := s.QuarantineAcknowledgementFor(ctx, q)
		require.NoError(t, err)
		require.NotNil(t, receipt, "no receipt for %q", id)
		if !utf8.ValidString(id) {
			continue // invalid UTF-8 ids are a documented boundary of this lookup (MTIX-187.7.15)
		}
		pending, err := s.ReadPendingEventsByID(ctx, []string{id})
		require.NoError(t, err)
		require.Len(t, pending, 1, "pending lookup lost %q", id)
	}
}
