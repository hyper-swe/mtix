// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

// Listing tests measure read queries and preserve ordered mixed receipt results.

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func newBatchListingFixture(tb testing.TB, count int) (*service.SyncService, *sqlite.Store, []service.QuarantinedEvent) {
	tb.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(filepath.Join(tb.TempDir(), "mtix.db"), logger)
	require.NoError(tb, err)
	tb.Cleanup(func() { require.NoError(tb, st.Close()) })
	clock := func() time.Time { return time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC) }
	st.SetClock(clock)
	svc := service.NewSyncService(st, logger, clock)
	want := []service.QuarantinedEvent{}
	require.NoError(tb, st.WithTx(tb.Context(), func(tx *sql.Tx) error {
		for n := 1; n <= count; n++ {
			id, source := fmt.Sprintf("evt-%04d", n), "pull"
			if n%2 == 0 {
				source = "sweep"
			}
			q := sqlite.QuarantinedEvent{EventID: id, Source: source, RawEvent: strings.Replace(quarantineAckInvalidRaw, quarantineAckEventID, id, 1), Reason: "invalid node id", FirstSeen: "2026-03-14T10:00:00Z", LastAttempt: "2026-03-14T10:00:00Z", CLIVersion: "test"}
			if err := sqlite.QuarantineEvent(tb.Context(), tx, q); err != nil {
				return err
			}
			want = append(want, service.QuarantinedEvent{EventID: id, Handle: service.QuarantineHandle(id), EventIDSHA256: service.QuarantineHandleFull(id), NodeID: "TEST-1é", OpType: "create_node", LamportClock: 1, Source: source, Reason: q.Reason, Attempts: 1, FirstSeen: q.FirstSeen, LastAttempt: q.LastAttempt, CLIVersion: q.CLIVersion})
		}
		return nil
	}))
	for n := 2; n < count; n += 3 {
		result, err := svc.AcknowledgeQuarantine(tb.Context(), want[n].EventID, "op-a", "reviewed "+want[n].EventID)
		require.NoError(tb, err)
		require.Equal(tb, want[n].Source, result.Source)
		want[n].Acknowledgement = &result.QuarantineAcknowledgement
	}
	return svc, st, want
}

func TestSyncServiceListQuarantined_BoundedReadQueriesAndIdenticalResults(t *testing.T) {
	for _, tc := range []struct {
		rows     int
		maxReads int64
	}{{0, 1}, {500, 3}, {501, 5}} {
		t.Run(fmt.Sprint(tc.rows), func(t *testing.T) {
			svc, st, want := newBatchListingFixture(t, tc.rows)
			// With no idle connections, each finished read query closes exactly one
			// connection. Stats counts these closes without a production query hook.
			st.ReadDB().SetMaxIdleConns(0)
			before := st.ReadDB().Stats().MaxIdleClosed
			got, err := svc.ListQuarantined(t.Context())
			require.NoError(t, err)
			reads := st.ReadDB().Stats().MaxIdleClosed - before
			t.Logf("%d rows: %d read queries", tc.rows, reads)
			require.Equal(t, want, got)
			require.LessOrEqual(t, reads, tc.maxReads, "receipt reads must be bounded per page")
		})
	}
}

func TestSyncServiceListQuarantined_ReceiptMatchesExactRawAndSource(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		args        []any
		source      string
	}{
		{"changed raw", "UPDATE sync_quarantine SET raw_event = raw_event || ' ' WHERE event_id = ?", []any{"evt-0003"}, "pull"},
		{"push source", "UPDATE sync_quarantine SET source = ? WHERE event_id = ?", []any{"push", "evt-0003"}, "push"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, want := newBatchListingFixture(t, 3)
			require.NotNil(t, want[2].Acknowledgement)
			_, err := st.WriteDB().ExecContext(t.Context(), tc.query, tc.args...)
			require.NoError(t, err)
			want[2].Acknowledgement = nil
			want[2].Source = tc.source
			got, err := svc.ListQuarantined(t.Context())
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
}

// BenchmarkSyncServiceListQuarantined_ManyRows uses the same 1000-row fixture
// before and after batching, with both pull/sweep and immutable receipts.
func BenchmarkSyncServiceListQuarantined_ManyRows(b *testing.B) {
	svc, _, want := newBatchListingFixture(b, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		rows, err := svc.ListQuarantined(b.Context())
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != len(want) {
			b.Fatalf("listed %d rows, want %d", len(rows), len(want))
		}
	}
}
