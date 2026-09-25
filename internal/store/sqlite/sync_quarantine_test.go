// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of the pulled-event quarantine's data access (MTIX-95.11): the
// local sync_quarantine table, the savepoint that isolates each pulled
// event's apply, and the local Lamport read the jump bound compares with.

// quarantineRow is a valid row whose raw event carries lamport.
func quarantineRow(id, raw string) sqlite.QuarantinedEvent {
	return sqlite.QuarantinedEvent{
		EventID: id, Source: "pull", RawEvent: raw, Reason: "first reason",
		FirstSeen: "2026-09-25T10:00:00Z", LastAttempt: "2026-09-25T10:00:00Z",
		CLIVersion: "0.5.4-test",
	}
}

// quarantineTx runs fn in one store transaction and requires it to commit.
func quarantineTx(t *testing.T, s *sqlite.Store, fn func(tx *sql.Tx) error) {
	t.Helper()
	require.NoError(t, s.WithTx(context.Background(), fn))
}

// TestSchema_FreshDBHasSyncQuarantine: the table has the ticket's columns,
// keyed by event_id, created without a schema version change.
func TestSchema_FreshDBHasSyncQuarantine(t *testing.T) {
	_, db := schemaTestEnv(t, filepath.Join(t.TempDir(), "fresh.db"))

	require.Equal(t, []string{"event_id", "source", "raw_event", "reason", "first_seen",
		"last_attempt", "attempts", "cli_version"}, columnsOf(t, db, "sync_quarantine"))
	insert := `INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen, last_attempt)
		VALUES ('e1', 'pull', '{}', 'r', 't', 't')`
	_, err := db.Exec(insert)
	require.NoError(t, err)
	_, err = db.Exec(insert)
	require.Error(t, err, "event_id is the primary key")
	var attempts int
	require.NoError(t, db.QueryRow(`SELECT attempts FROM sync_quarantine WHERE event_id = 'e1'`).Scan(&attempts))
	require.Equal(t, 1, attempts, "a first quarantine counts one attempt")
	_, err = db.Exec(`INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen, last_attempt)
		VALUES ('e2', 'elsewhere', '{}', 'r', 't', 't')`)
	require.Error(t, err, "source is pull or sweep")
}

// TestQuarantineEvent_SameEventAgain_CountsAttemptKeepsFirstSeen: a first
// quarantine inserts the row with one attempt; a later failure of the same
// event only counts the attempt and moves last_attempt, and rewrites
// nothing else: first_seen, source, the raw event, the reason and the CLI
// version stay as first recorded (MTIX-95.11 round 2).
func TestQuarantineEvent_SameEventAgain_CountsAttemptKeepsFirstSeen(t *testing.T) {
	s := newTestStore(t)
	first := quarantineRow("e1", `{"lamport_clock":1}`)
	again := sqlite.QuarantinedEvent{EventID: "e1", Source: "sweep", RawEvent: `{"other":true}`,
		Reason: "second reason", FirstSeen: "2026-09-26T00:00:00Z",
		LastAttempt: "2026-09-26T00:00:00Z", CLIVersion: "0.5.5"}

	quarantineTx(t, s, func(tx *sql.Tx) error { return sqlite.QuarantineEvent(context.Background(), tx, first) })
	quarantineTx(t, s, func(tx *sql.Tx) error { return sqlite.QuarantineEvent(context.Background(), tx, again) })

	page, err := s.QuarantinePage(context.Background(), nil, 10)
	require.NoError(t, err)
	require.Len(t, page, 1)
	got := page[0]
	require.Equal(t, "pull", got.Source)
	require.Equal(t, `{"lamport_clock":1}`, got.RawEvent)
	require.Equal(t, "2026-09-25T10:00:00Z", got.FirstSeen)
	require.Equal(t, "first reason", got.Reason)
	require.Equal(t, "2026-09-26T00:00:00Z", got.LastAttempt)
	require.Equal(t, "0.5.4-test", got.CLIVersion)
	require.Equal(t, 2, got.Attempts)
	require.Equal(t, int64(1), got.Lamport)
	n, err := s.CountQuarantined(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

// TestRemoveQuarantined_RemovesOnlyThatEvent: removing one id leaves the
// others; removing an id that is not quarantined is a no-op.
func TestRemoveQuarantined_RemovesOnlyThatEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	quarantineTx(t, s, func(tx *sql.Tx) error {
		for _, id := range []string{"e1", "e2"} {
			if err := sqlite.QuarantineEvent(ctx, tx, quarantineRow(id, `{"lamport_clock":1}`)); err != nil {
				return err
			}
		}
		return nil
	})

	quarantineTx(t, s, func(tx *sql.Tx) error { return sqlite.RemoveQuarantined(ctx, tx, "e1") })
	quarantineTx(t, s, func(tx *sql.Tx) error { return sqlite.RemoveQuarantined(ctx, tx, "absent") })

	page, err := s.QuarantinePage(ctx, nil, 10)
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "e2", page[0].EventID)
}

// TestQuarantinePage_KeysetInLamportOrder: rows come in (Lamport clock of
// the raw event, event id) order, limit at a time, resuming after the given
// key. A raw event without a readable Lamport clock sorts as 0; a negative
// clock (an event that failed validation) sorts first.
func TestQuarantinePage_KeysetInLamportOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rows := map[string]string{
		"b-five":  `{"lamport_clock":5}`,
		"a-five":  `{"lamport_clock":5}`,
		"one":     `{"lamport_clock":1}`,
		"neg":     `{"lamport_clock":-2}`,
		"garbled": `not json`,
		"no-key":  `{"op_type":"claim"}`,
	}
	quarantineTx(t, s, func(tx *sql.Tx) error {
		for id, raw := range rows {
			if err := sqlite.QuarantineEvent(ctx, tx, quarantineRow(id, raw)); err != nil {
				return err
			}
		}
		return nil
	})

	var got []string
	var after *sqlite.QuarantineKey
	pages := 0
	for {
		page, err := s.QuarantinePage(ctx, after, 2)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		pages++
		require.LessOrEqual(t, len(page), 2)
		for _, q := range page {
			got = append(got, q.EventID)
		}
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}

	require.Equal(t, []string{"neg", "garbled", "no-key", "one", "a-five", "b-five"}, got)
	require.Equal(t, 3, pages)
	n, err := s.CountQuarantined(ctx)
	require.NoError(t, err)
	require.Equal(t, len(rows), n)
}

// TestLocalLamport_ReadsMetaSyncLamport: the local clock the jump bound
// compares with, read in the caller's transaction; a value that is not an
// integer is an error.
func TestLocalLamport_ReadsMetaSyncLamport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE meta SET value = '41' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)
	var got int64
	quarantineTx(t, s, func(tx *sql.Tx) error {
		var readErr error
		got, readErr = sqlite.LocalLamport(ctx, tx)
		return readErr
	})
	require.Equal(t, int64(41), got)

	_, err = s.WriteDB().ExecContext(ctx, `UPDATE meta SET value = 'x' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		_, readErr := sqlite.LocalLamport(ctx, tx)
		return readErr
	})
	require.ErrorContains(t, err, "meta.sync.lamport")
}

// TestWithSavepoint_FnFails_RollsBackOnlyItsWrites: a failing fn has its own
// writes rolled back and its error returned as rolledBack, while the
// transaction stays usable and the writes made before the savepoint commit;
// a successful fn's writes commit. A savepoint on a finished transaction
// reports err, not rolledBack.
func TestWithSavepoint_FnFails_RollsBackOnlyItsWrites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	boom := errors.New("apply failed")
	insert := func(tx *sql.Tx, id string) error {
		return sqlite.QuarantineEvent(ctx, tx, quarantineRow(id, `{"lamport_clock":1}`))
	}

	quarantineTx(t, s, func(tx *sql.Tx) error {
		if err := insert(tx, "before"); err != nil {
			return err
		}
		rolledBack, err := sqlite.WithSavepoint(ctx, tx, func() error {
			if err := insert(tx, "inside-failed"); err != nil {
				return err
			}
			return boom
		})
		require.NoError(t, err)
		require.ErrorIs(t, rolledBack, boom)
		rolledBack, err = sqlite.WithSavepoint(ctx, tx, func() error { return insert(tx, "inside-ok") })
		require.NoError(t, err)
		require.NoError(t, rolledBack)
		return insert(tx, "after")
	})

	page, err := s.QuarantinePage(ctx, nil, 10)
	require.NoError(t, err)
	var ids []string
	for _, q := range page {
		ids = append(ids, q.EventID)
	}
	require.ElementsMatch(t, []string{"before", "inside-ok", "after"}, ids)

	tx, err := s.WriteDB().BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	rolledBack, err := sqlite.WithSavepoint(ctx, tx, func() error { return nil })
	require.Error(t, err, "the savepoint statement itself fails on a finished transaction")
	require.NoError(t, rolledBack)
}

// TestQuarantinePage_NodeAndOpFromRawEvent: a page carries each raw event's
// node_id and op_type for `mtix sync quarantine list`; empty when the raw
// event is not JSON or lacks them.
func TestQuarantinePage_NodeAndOpFromRawEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	quarantineTx(t, s, func(tx *sql.Tx) error {
		if err := sqlite.QuarantineEvent(ctx, tx, quarantineRow("a",
			`{"lamport_clock":1,"node_id":"PROJ-7","op_type":"link_dep"}`)); err != nil {
			return err
		}
		return sqlite.QuarantineEvent(ctx, tx, quarantineRow("b", `not json`))
	})

	page, err := s.QuarantinePage(ctx, nil, 10)

	require.NoError(t, err)
	require.Len(t, page, 2)
	require.Equal(t, "b", page[0].EventID)
	require.Empty(t, page[0].NodeID)
	require.Empty(t, page[0].OpType)
	require.Equal(t, "PROJ-7", page[1].NodeID)
	require.Equal(t, "link_dep", page[1].OpType)
}

// TestIsQuarantinedAndEventApplied_ReportLocalCopies: IsQuarantined finds a
// quarantine row; EventApplied finds only an id in applied_events. An own
// event that is only in sync_events is not applied yet: its hub copy is
// still checked (MTIX-95.11 round 4). Neither finds an unknown id.
func TestIsQuarantinedAndEventApplied_ReportLocalCopies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx,
		`INSERT INTO applied_events (event_id, applied_at, applied_by_lamport) VALUES ('applied', 't', 1)`)
	require.NoError(t, err)
	quarantineTx(t, s, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, quarantineRow("held-q", `{}`))
	})
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "own node", time.Now().UTC())))
	var own string
	require.NoError(t, s.QueryRow(ctx, `SELECT event_id FROM sync_events LIMIT 1`).Scan(&own))

	quarantineTx(t, s, func(tx *sql.Tx) error {
		for id, want := range map[string][2]bool{
			"held-q": {true, false}, "applied": {false, true}, own: {false, false}, "unknown": {false, false},
		} {
			q, err := sqlite.IsQuarantined(ctx, tx, id)
			require.NoError(t, err)
			require.Equalf(t, want[0], q, "IsQuarantined(%s)", id)
			h, err := sqlite.EventApplied(ctx, tx, id)
			require.NoError(t, err)
			require.Equalf(t, want[1], h, "EventApplied(%s)", id)
		}
		return nil
	})
}

// TestClearQuarantine_EmptiesTable: clone's reset empties the quarantine
// (the clone rebuilds the store from the hub).
func TestClearQuarantine_EmptiesTable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	quarantineTx(t, s, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, quarantineRow("e1", `{}`))
	})

	require.NoError(t, s.ClearQuarantine(ctx))

	n, err := s.CountQuarantined(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestDiscardLocal_ClearsQuarantine: `mtix sync reconcile --discard-local`
// takes the hub as the ground truth, so it drops the quarantine with the
// rest of the local sync state.
func TestDiscardLocal_ClearsQuarantine(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t)
	ctx := context.Background()
	quarantineTx(t, s, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, quarantineRow("e1", `{}`))
	})

	require.NoError(t, sqlite.DiscardLocal(ctx, s, dir))

	n, err := s.CountQuarantined(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

// TestLocalLamportClock_ReadsOutsideATransaction: the local clock as the
// pull loop reads it after a batch, for the cursor decision.
func TestLocalLamportClock_ReadsOutsideATransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `UPDATE meta SET value = '9' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)

	got, err := s.LocalLamportClock(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(9), got)

	_, err = s.WriteDB().ExecContext(ctx, `UPDATE meta SET value = 'x' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)
	_, err = s.LocalLamportClock(ctx)
	require.ErrorContains(t, err, "meta.sync.lamport")
}
