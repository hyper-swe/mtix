// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// Hostile event id proof for the sqlite sync store (MTIX-187.7.2, child B of
// MTIX-187.7; founder decision MTIX-187.6: ids are accepted as received and
// stored byte for byte). Every test runs over testutil.HostileIDs(). A
// fixture may be excused from a surface only through hostileBoundary, which
// names the follow-up ticket; TestHostileBoundary_ListIsNotStale fails when a
// listed pair starts to pass, so the list cannot rot. None of these tests
// asserts error wording, and none refuses a fixture: this change adds no id
// refusal.

// hostileWireLimit is the push wire cap; emit refuses a longer id with a
// typed error (sync_emit_wirecap.go), so the long fixture is not applied.
const hostileWireLimit = 64 * 1024

// hostileBoundary maps "surface/fixture" to the ticket that owns the
// difference between the stored id and the id read back on that surface.
// MTIX-187.7.15: JSON copies of an event (json.Marshal and json_each over a
// marshalled list) replace invalid UTF-8 with U+FFFD, so a surface that
// carries the id through JSON cannot return the original bytes.
var hostileBoundary = func() map[string]string {
	out := map[string]string{}
	for _, surface := range []string{"export_json"} {
		for _, fixture := range []string{"invalid_utf8_lone_continuation", "invalid_utf8_truncated"} {
			out[hostileKey(surface, fixture)] = "MTIX-187.7.15"
		}
	}
	return out
}()

// requireSurface judges one surface for one fixture. A pair listed in
// hostileBoundary must still be lossy: when it round-trips, the entry is
// stale and the test fails until the entry is removed. Any other pair must
// round-trip byte-exact.
func requireSurface(t *testing.T, surface, fixture string, exact bool, detail string) {
	t.Helper()
	ticket, listed := hostileBoundary[hostileKey(surface, fixture)]
	switch {
	case listed && exact:
		t.Fatalf("stale hostileBoundary entry %s/%s (%s): it now round-trips; remove the entry", surface, fixture, ticket)
	case listed:
		t.Logf("known boundary %s: %s", ticket, detail)
	default:
		require.True(t, exact, "%s: %s", surface, detail)
	}
}

func hostileStore(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hostile.db")
	s := sqlite.FixtureStoreForTest(t, path)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	return s, raw
}

// hostileCreate is a valid create_node event whose event id is id.
func hostileCreate(t *testing.T, id, node string, lamport int64) *model.SyncEvent {
	t.Helper()
	payload, err := model.EncodePayload(&model.CreateNodePayload{Title: "hostile"})
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID: id, ProjectPrefix: "MTIX", NodeID: node, OpType: model.OpCreateNode,
		Payload: payload, WallClockTS: 1_700_000_000_000, LamportClock: lamport,
		VectorClock: model.VectorClock{"alice": lamport}, AuthorID: "alice",
		AuthorMachineHash: "0123456789abcdef", SyncStatus: model.SyncStatusApplied,
	}
}

func hostileApply(t *testing.T, s *sqlite.Store, e *model.SyncEvent) error {
	t.Helper()
	return s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return s.IdempotentApply(context.Background(), tx, e)
	})
}

func hostileApplied(t *testing.T, s *sqlite.Store, id string) (applied bool) {
	t.Helper()
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) (err error) {
		applied, err = sqlite.EventApplied(context.Background(), tx, id)
		return err
	}))
	return applied
}

// idsIn returns the event ids stored in table.column, as raw bytes.
func idsIn(t *testing.T, raw *sql.DB, query string, args ...any) [][]byte {
	t.Helper()
	rows, err := raw.Query(query, args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out [][]byte
	for rows.Next() {
		var b []byte
		require.NoError(t, rows.Scan(&b))
		out = append(out, b)
	}
	require.NoError(t, rows.Err())
	return out
}

// storedByHexQueries are the constant lookups storedByHex may run, one per
// table, so no SQL is built from a name.
var storedByHexQueries = map[string]string{
	"applied_events":  `SELECT event_id FROM applied_events WHERE hex(event_id) = ?`,
	"sync_events":     `SELECT event_id FROM sync_events WHERE hex(event_id) = ?`,
	"sync_quarantine": `SELECT event_id FROM sync_quarantine WHERE hex(event_id) = ?`,
}

// storedByHex reads back the id bytes of the rows of table whose event_id
// has exactly the bytes of id. The comparison is on hex(event_id), so it does
// not depend on text collation, LIKE or trimming; the returned bytes are what
// the column holds.
func storedByHex(t *testing.T, raw *sql.DB, table, id string) [][]byte {
	t.Helper()
	query, ok := storedByHexQueries[table]
	require.True(t, ok, "no query for table %q", table)
	return idsIn(t, raw, query, strings.ToUpper(hex.EncodeToString([]byte(id))))
}

func singleID(t *testing.T, got [][]byte, want string) {
	t.Helper()
	require.Len(t, got, 1)
	require.True(t, bytes.Equal(got[0], []byte(want)), "stored id %q differs from %q", got[0], want)
}

func TestSyncApply_HostileEventIDs_RoundTripByteExact(t *testing.T) {
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the push wire cap; covered by the emit cap tests")
			}
			s, raw := hostileStore(t)
			e := hostileCreate(t, fx.Value, "MTIX-1", 1)
			require.NoError(t, hostileApply(t, s, e))

			singleID(t, idsIn(t, raw, `SELECT event_id FROM applied_events`), fx.Value)
			singleID(t, idsIn(t, raw, `SELECT event_id FROM sync_events`), fx.Value)
			require.True(t, hostileApplied(t, s, fx.Value), "the exact id reads back as applied")
			// Every history row a replay writes carries the exact event id.
			for _, id := range idsIn(t, raw, `SELECT event_id FROM task_history WHERE event_id IS NOT NULL`) {
				require.True(t, bytes.Equal(id, []byte(fx.Value)), "task_history.event_id %q differs from %q", id, fx.Value)
			}
		})
	}
}

func TestSyncApply_HostileEventID_ReapplyIsIdempotent(t *testing.T) {
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the push wire cap")
			}
			s, raw := hostileStore(t)
			e := hostileCreate(t, fx.Value, "MTIX-1", 1)
			require.NoError(t, hostileApply(t, s, e))
			before := rowCounts(t, raw)

			require.NoError(t, hostileApply(t, s, e), "the same event applies as a no-op")
			require.NoError(t, hostileApply(t, s, hostileCreate(t, fx.Value, "MTIX-1", 1)),
				"a re-delivered copy of the event applies as a no-op")
			require.Equal(t, before, rowCounts(t, raw), "no row is added or removed by a re-apply")
			singleID(t, idsIn(t, raw, `SELECT event_id FROM applied_events`), fx.Value)

			// A later local edit must survive a re-delivery of the old create.
			_, err := raw.Exec(`UPDATE nodes SET title = 'edited later' WHERE id = 'MTIX-1'`)
			require.NoError(t, err)
			require.NoError(t, hostileApply(t, s, hostileCreate(t, fx.Value, "MTIX-1", 1)))
			var title string
			require.NoError(t, raw.QueryRow(`SELECT title FROM nodes WHERE id = 'MTIX-1'`).Scan(&title))
			require.Equal(t, "edited later", title, "a re-delivered event does not run again")
			require.Equal(t, before, rowCounts(t, raw))
		})
	}
}

// rowCounts counts the rows an apply touches, so a re-apply that adds one is
// visible: nodes, applied events, the sync log and the change history.
// rowCountQueries are the constant counts rowCounts runs, one per table.
var rowCountQueries = map[string]string{
	"nodes":          `SELECT COUNT(*) FROM nodes`,
	"applied_events": `SELECT COUNT(*) FROM applied_events`,
	"sync_events":    `SELECT COUNT(*) FROM sync_events`,
	"task_history":   `SELECT COUNT(*) FROM task_history`,
}

func rowCounts(t *testing.T, raw *sql.DB) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"nodes", "applied_events", "sync_events", "task_history"} {
		var n int
		require.NoError(t, raw.QueryRow(rowCountQueries[table]).Scan(&n))
		out[table] = n
	}
	return out
}

// hostileQuarantineRow is a pulled-event quarantine row for id. RawEvent is a JSON
// copy of the event, as the pull builds it.
func hostileQuarantineRow(t *testing.T, id string) sqlite.QuarantinedEvent {
	t.Helper()
	rawEvent, err := json.Marshal(hostileCreate(t, id, "MTIX-1", 1))
	require.NoError(t, err)
	return sqlite.QuarantinedEvent{
		EventID: id, Source: "pull", RawEvent: string(rawEvent), Reason: "invalid node id",
		FirstSeen: "2026-03-14T10:00:00Z", LastAttempt: "2026-03-14T10:00:00Z", CLIVersion: "test",
	}
}

func hostileQuarantine(t *testing.T, s *sqlite.Store, q sqlite.QuarantinedEvent) {
	t.Helper()
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(context.Background(), tx, q)
	}))
}

func hostileQuarantinedIDs(t *testing.T, s *sqlite.Store) [][]byte {
	t.Helper()
	page, err := s.QuarantinePage(context.Background(), nil, 1000)
	require.NoError(t, err)
	out := make([][]byte, 0, len(page))
	for _, q := range page {
		out = append(out, []byte(q.EventID))
	}
	return out
}

func TestQuarantine_HostileEventIDs_PersistAndAckByBytes(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the push wire cap")
			}
			s, raw := hostileStore(t)
			q := hostileQuarantineRow(t, fx.Value)
			hostileQuarantine(t, s, q)
			hostileQuarantine(t, s, q)

			singleID(t, idsIn(t, raw, `SELECT event_id FROM sync_quarantine`), fx.Value)
			singleID(t, hostileQuarantinedIDs(t, s), fx.Value)
			var attempts int
			require.NoError(t, raw.QueryRow(`SELECT attempts FROM sync_quarantine WHERE event_id = ?`, fx.Value).Scan(&attempts))
			require.Equal(t, 2, attempts, "a repeat counts an attempt on the same row")

			got, err := s.QuarantineForAcknowledgement(ctx, fx.Value)
			require.NoError(t, err)
			require.Equal(t, q.RawEvent, got.RawEvent)
			res, err := s.RecordQuarantineAcknowledgement(ctx, got, sqlite.QuarantineAcknowledgement{
				EventID: fx.Value, RawEventSHA256: sha256Hex(got.RawEvent), Source: got.Source, Actor: "op",
				Reason: "reviewed", AcknowledgedAt: "2026-03-14T11:00:00Z",
			})
			require.NoError(t, err)
			require.False(t, res.AlreadyAcknowledged)
			require.Equal(t, fx.Value, res.EventID, "the receipt carries the exact id")
			receipt, err := s.QuarantineAcknowledgementFor(ctx, got)
			require.NoError(t, err)
			require.NotNil(t, receipt)
			require.Equal(t, fx.Value, receipt.EventID)

			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
				in, err := sqlite.IsQuarantined(ctx, tx, fx.Value)
				require.NoError(t, err)
				require.True(t, in)
				return sqlite.RemoveQuarantined(ctx, tx, fx.Value)
			}))
			require.Empty(t, hostileQuarantinedIDs(t, s), "removal by the exact id removes the row")
		})
	}
}

// TestQuarantine_HostileEventIDs_NeverFoldTogether quarantines every fixture
// into one store: no id may match, replace or remove another through
// LIKE wildcards, case or Unicode folding, trimming or NUL truncation.
func TestQuarantine_HostileEventIDs_NeverFoldTogether(t *testing.T) {
	ctx := context.Background()
	s, _ := hostileStore(t)
	var want []string
	for _, fx := range testutil.HostileIDs() {
		if len(fx.Value) > hostileWireLimit {
			continue
		}
		hostileQuarantine(t, s, hostileQuarantineRow(t, fx.Value))
		want = append(want, fx.Value)
	}
	require.Len(t, hostileQuarantinedIDs(t, s), len(want), "no id folded into another")
	// Every id is looked up and acknowledged while all the others are present,
	// so a lookup that folds (LIKE, trim, case) returns another id's row.
	for _, id := range want {
		got, err := s.QuarantineForAcknowledgement(ctx, id)
		require.NoError(t, err)
		require.Equal(t, hostileQuarantineRow(t, id).RawEvent, got.RawEvent, "the ack lookup of %q returned another row", id)
		res, err := s.RecordQuarantineAcknowledgement(ctx, got, sqlite.QuarantineAcknowledgement{
			EventID: id, RawEventSHA256: sha256Hex(got.RawEvent), Source: got.Source, Actor: "op",
			Reason: "reviewed", AcknowledgedAt: "2026-03-14T11:00:00Z",
		})
		require.NoError(t, err)
		require.True(t, bytes.Equal([]byte(id), []byte(res.EventID)))
		receipt, err := s.QuarantineAcknowledgementFor(ctx, got)
		require.NoError(t, err)
		require.NotNil(t, receipt)
		require.True(t, bytes.Equal([]byte(id), []byte(receipt.EventID)))
	}
	for _, id := range want {
		before := len(hostileQuarantinedIDs(t, s))
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			return sqlite.RemoveQuarantined(ctx, tx, id)
		}))
		left := hostileQuarantinedIDs(t, s)
		require.Len(t, left, before-1, "removing %q removes exactly its own row", id)
		for _, l := range left {
			require.NotEqual(t, id, string(l), "removed by exact bytes")
		}
	}
	require.Empty(t, hostileQuarantinedIDs(t, s))
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// pendingFixture applies a create for id and marks its log row pending, so
// the push reads see it the way an own unpushed event looks.
func pendingFixture(t *testing.T, s *sqlite.Store, raw *sql.DB, id string) {
	t.Helper()
	require.NoError(t, hostileApply(t, s, hostileCreate(t, id, "MTIX-1", 1)))
	res, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pending' WHERE event_id = ?`, id)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

func TestSyncPending_HostileEventIDs_ReadBackByteExact(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the push wire cap")
			}
			s, raw := hostileStore(t)
			pendingFixture(t, s, raw, fx.Value)

			all, err := s.ReadPendingEvents(ctx, 10)
			require.NoError(t, err)
			require.Len(t, all, 1)
			require.True(t, bytes.Equal([]byte(fx.Value), []byte(all[0].EventID)), "ReadPendingEvents returned %q", all[0].EventID)

			byID, err := s.ReadPendingEventsByID(ctx, []string{fx.Value})
			require.NoError(t, err)
			requireSurface(t, "pending_by_id", fx.Name,
				len(byID) == 1 && bytes.Equal([]byte(fx.Value), []byte(byID[0].EventID)),
				"the exact id does not select its pending event")

			after, err := s.ReadPendingEventsAfter(ctx, 0, "", 10)
			require.NoError(t, err)
			require.Len(t, after, 1)
			past, err := s.ReadPendingEventsAfter(ctx, all[0].LamportClock, fx.Value, 10)
			require.NoError(t, err)
			require.Empty(t, past, "the cursor at the exact id is past the event")
		})
	}
}

// hostileSurface is one place an id is read back; hostileBoundary keys are
// "surface/fixture".
func hostileKey(surface, fixture string) string { return surface + "/" + fixture }

// TestSyncPush_HostileEventIDs_PushSurfacesKeepIDs judges the push-side
// surfaces that carry ids through json_each: PushSubjects, ReleasePushHolds
// and the uid lookup behind EventsAwaitingCreation.
func TestSyncPush_HostileEventIDs_PushSurfacesKeepIDs(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the push wire cap")
			}
			s, raw := hostileStore(t)
			pendingFixture(t, s, raw, fx.Value)

			subjects, err := s.PushSubjects(ctx, []string{fx.Value})
			require.NoError(t, err)
			subj, found := subjects[fx.Value]
			requireSurface(t, "push_subjects", fx.Name, found && subj.TaskNodeID == "MTIX-1",
				"the exact id has no push subject")

			// An event about the task by uid, naming a stale number, waits for the
			// pending creation only when the uid lookup finds the task.
			about := hostileCreate(t, "ev-about", "MTIX-9", 2)
			about.OpType, about.UID = model.OpUpdateField, fx.Value
			waiting, err := s.EventsAwaitingCreation(ctx, []*model.SyncEvent{about})
			require.NoError(t, err)
			_, waits := waiting["ev-about"]
			requireSurface(t, "events_awaiting_creation", fx.Name, waits,
				"the uid of the task is not found, so the event does not wait")

			hold := hostileQuarantineRow(t, fx.Value)
			require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{hold}, "test"))
			held, err := s.CountHeldPushEvents(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, held)
			require.NoError(t, s.ReleasePushHolds(ctx, []string{fx.Value}))
			held, err = s.CountHeldPushEvents(ctx)
			require.NoError(t, err)
			requireSurface(t, "release_push_holds", fx.Name, held == 0, "the hold is not released and stays stuck")
		})
	}
}

func TestSyncStore_HostileEventIDs_ExportImportKeepsIDs(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the push wire cap")
			}
			src, _ := hostileStore(t)
			require.NoError(t, hostileApply(t, src, hostileCreate(t, fx.Value, "MTIX-1", 1)))
			data, err := src.Export(ctx, "MTIX", "test")
			require.NoError(t, err)
			require.Len(t, data.Nodes, 1)
			require.Equal(t, fx.Value, data.Nodes[0].UID, "the create event id is the node uid (self-anchor)")

			// The export file is JSON: carry the data through it, as a file would.
			encoded, err := json.Marshal(data)
			require.NoError(t, err)
			var fromFile sqlite.ExportData
			require.NoError(t, json.Unmarshal(encoded, &fromFile))

			dst, dstRaw := hostileStore(t)
			_, err = dst.Import(ctx, &fromFile, sqlite.ImportModeReplace, true)
			require.NoError(t, err)
			got := idsIn(t, dstRaw, `SELECT uid FROM nodes WHERE id = 'MTIX-1'`)
			require.Len(t, got, 1)
			requireSurface(t, "export_json", fx.Name, bytes.Equal(got[0], []byte(fx.Value)),
				"the id through the JSON export file changed")
		})
	}
}

func TestSyncStore_HostileEventIDs_IntegrityCheckOK(t *testing.T) {
	ctx := context.Background()
	s, raw := hostileStore(t)
	var ids []string
	n := int64(0)
	for _, fx := range testutil.HostileIDs() {
		if len(fx.Value) > hostileWireLimit {
			continue
		}
		n++
		ids = append(ids, fx.Value)
		require.NoError(t, hostileApply(t, s, hostileCreate(t, fx.Value, "MTIX-"+hostileItoa(n), n)))
		hostileQuarantine(t, s, hostileQuarantineRow(t, fx.Value+"-q"))
	}
	// Run the other operations on the same store before the checks: pending
	// reads, push holds and releases, acknowledgements, removals and an import.
	for _, id := range ids {
		_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pending' WHERE event_id = ?`, id)
		require.NoError(t, err)
	}
	_, err := s.ReadPendingEvents(ctx, 1000)
	require.NoError(t, err)
	_, err = s.ReadPendingEventsByID(ctx, ids)
	require.NoError(t, err)
	_, err = s.PushSubjects(ctx, ids)
	require.NoError(t, err)
	require.NoError(t, s.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{hostileQuarantineRow(t, ids[0])}, "test"))
	require.NoError(t, s.ReleasePushHolds(ctx, ids))
	for _, id := range ids {
		q := hostileQuarantineRow(t, id+"-q")
		got, err := s.QuarantineForAcknowledgement(ctx, q.EventID)
		require.NoError(t, err)
		_, err = s.RecordQuarantineAcknowledgement(ctx, got, sqlite.QuarantineAcknowledgement{
			EventID: q.EventID, RawEventSHA256: sha256Hex(got.RawEvent), Source: got.Source, Actor: "op",
			Reason: "reviewed", AcknowledgedAt: "2026-03-14T11:00:00Z",
		})
		require.NoError(t, err)
	}
	for _, id := range ids[:len(ids)/2] {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			return sqlite.RemoveQuarantined(ctx, tx, id+"-q")
		}))
	}
	data, err := s.Export(ctx, "MTIX", "test")
	require.NoError(t, err)
	_, err = s.Import(ctx, data, sqlite.ImportModeMerge, true)
	require.NoError(t, err)

	res, err := s.Verify(ctx)
	require.NoError(t, err)
	require.True(t, res.AllPassed, "overall verdict: %+v", res)
	require.True(t, res.IntegrityOK, "PRAGMA integrity_check")
	require.True(t, res.ForeignKeyOK)
	require.True(t, res.UIDUniqueOK)
	require.Empty(t, res.Errors)

	var verdict string
	require.NoError(t, raw.QueryRow(`PRAGMA integrity_check`).Scan(&verdict))
	require.Equal(t, "ok", verdict)
}

func hostileItoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestHostileBoundary_ListIsNotStale pins the shape of the list: every entry
// names a real fixture of the invalid UTF-8 class and the follow-up ticket.
// requireSurface fails the surface tests when an entry starts to pass.
func TestHostileBoundary_ListIsNotStale(t *testing.T) {
	byName := map[string]testutil.HostileClass{}
	for _, fx := range testutil.HostileIDs() {
		byName[fx.Name] = fx.Class
	}
	for key, ticket := range hostileBoundary {
		surface, fixture, ok := strings.Cut(key, "/")
		require.True(t, ok, key)
		require.Contains(t, []string{"export_json"}, surface, key)
		require.Equal(t, testutil.ClassInvalidUTF8, byName[fixture], "%s must name an invalid UTF-8 fixture", key)
		require.Equal(t, "MTIX-187.7.15", ticket, key)
	}
	require.NotEmpty(t, hostileBoundary)
}

// FuzzSyncEventIDRoundTrip feeds arbitrary event ids through apply and the
// quarantine of one shared store. For every input the outcome is either a
// byte-exact round trip, or a typed refusal (model.ErrInvalidInput) that
// leaves the store unchanged; never a panic, a truncated or changed id, or a
// failed integrity check at the end. Plain `go test` runs only the seed
// corpus (every testutil.HostileIDs value below the wire cap, plus a few
// shapes); active fuzzing is opt-in:
//
//	go test -run '^$' -fuzz FuzzSyncEventIDRoundTrip -fuzztime 20s ./internal/store/sqlite
func FuzzSyncEventIDRoundTrip(f *testing.F) {
	for _, fx := range testutil.HostileIDs() {
		if len(fx.Value) <= hostileWireLimit {
			f.Add(fx.Value)
		}
	}
	f.Add("")
	f.Add("a\x00b\x00")
	f.Add("\xff\xfe")

	path := filepath.Join(f.TempDir(), "fuzz.db")
	s, err := sqlite.New(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		f.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() {
		res, verr := s.Verify(context.Background())
		if verr != nil || !res.IntegrityOK || !res.UIDUniqueOK {
			f.Errorf("integrity after fuzzing: %v %+v", verr, res)
		}
		if cerr := raw.Close(); cerr != nil {
			f.Errorf("close raw handle: %v", cerr)
		}
		if cerr := s.Close(); cerr != nil {
			f.Errorf("close store: %v", cerr)
		}
	})
	next := int64(0)

	f.Fuzz(func(t *testing.T, id string) {
		if id == "" || len(id) > hostileWireLimit {
			t.Skip("outside the contract: empty or over the wire cap")
		}
		next++
		before := rowCounts(t, raw)
		err := hostileApply(t, s, hostileCreate(t, id, "MTIX-"+hostileItoa(next), next))
		if err != nil {
			require.ErrorIs(t, err, model.ErrInvalidInput, "a refusal must be typed")
			require.Equal(t, before, rowCounts(t, raw), "a refused event leaves the store unchanged")
			return
		}
		for _, table := range []string{"applied_events", "sync_events"} {
			stored := storedByHex(t, raw, table, id)
			require.Len(t, stored, 1, "%s holds exactly one row with the input bytes", table)
			require.True(t, bytes.Equal(stored[0], []byte(id)), "%s stored %q for input %q", table, stored[0], id)
		}
		require.True(t, hostileApplied(t, s, id))
		require.NoError(t, hostileApply(t, s, hostileCreate(t, id, "MTIX-"+hostileItoa(next), next)),
			"re-apply is a no-op")

		hostileQuarantine(t, s, hostileQuarantineRow(t, id))
		stored := storedByHex(t, raw, "sync_quarantine", id)
		require.Len(t, stored, 1, "sync_quarantine holds exactly one row with the input bytes")
		require.True(t, bytes.Equal(stored[0], []byte(id)), "sync_quarantine stored %q for input %q", stored[0], id)
		got, qerr := s.QuarantineForAcknowledgement(context.Background(), id)
		require.NoError(t, qerr)
		require.Equal(t, hostileQuarantineRow(t, id).RawEvent, got.RawEvent)
		require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
			return sqlite.RemoveQuarantined(context.Background(), tx, id)
		}))
	})
}

// jsonEachParam matches json_each over any bound-parameter form: ?, ?1, $name,
// :name, @name, with optional spaces.
var jsonEachParam = regexp.MustCompile(`json_each\(\s*[?$:@]\w*\s*\)`)

// hexListRead matches the one accepted way to read a bound id list: the
// hex-decoding subquery over THAT json_each (idListSubquery in id_list.go).
var hexListRead = regexp.MustCompile(`CAST\(\s*unhex\(\s*value\s*\)\s+AS\s+TEXT\s*\)(?:\s+AS\s+\w+)?\s+FROM\s+json_each\(\s*[?$:@]\w*\s*\)`)

// receiptListRead matches the quarantine-receipt lookup, which reads its
// hex fields with unhex(json_extract(lookup.value, ...)) (MTIX-187.2.7).
var receiptListRead = regexp.MustCompile(`json_each\(\s*[?$:@]\w*\s*\)\s+AS\s+lookup[\s\S]*?unhex\(\s*json_extract\(\s*lookup\.value`)

// TestSQLiteIDLists_NeverBindPlainJSONLists is the guard for MTIX-187.7.15:
// json.Marshal changes invalid UTF-8, so a statement that reads a bound list
// through json_each must decode a hex list with unhex over that very
// json_each. It scans every non-test Go file of the repository: each string
// literal's json_each(<parameter>) count must equal the count of accepted
// reads. Labels and annotations bind no parameter, so nothing is allowlisted.
func TestSQLiteIDLists_NeverBindPlainJSONLists(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	fset := token.NewFileSet()
	var bad []string
	seen, scanned := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".hyperswe", ".mgit", "node_modules", "vendor", "web":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, perr := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, perr)
		scanned++
		sites, n := plainJSONListSites(parsed)
		seen += n
		for _, pos := range sites {
			bad = append(bad, fset.Position(pos).String())
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scanned, 200, "the scan must see the repository")
	require.GreaterOrEqual(t, seen, 9, "the scan must see the json_each statements")
	require.Empty(t, bad, "a bound list read with json_each must be hex encoded and decoded with unhex over that json_each")
}

// plainJSONListSites returns the positions of string literals in which a
// json_each over a bound parameter is not read through an accepted hex
// decoding, and how many literals use json_each over a parameter.
func plainJSONListSites(file *ast.File) (sites []token.Pos, seen int) {
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		eachCount := len(jsonEachParam.FindAllString(lit.Value, -1))
		if eachCount == 0 {
			return true
		}
		seen++
		accepted := len(hexListRead.FindAllString(lit.Value, -1)) + len(receiptListRead.FindAllString(lit.Value, -1))
		if accepted != eachCount {
			sites = append(sites, lit.Pos())
		}
		return true
	})
	return sites, seen
}

func TestPlainJSONListSites_FlagsEveryBypassAndAcceptsOnlyHexReads(t *testing.T) {
	tests := []struct {
		name string
		stmt string
		want int
	}{
		{"plain list", "`SELECT 1 FROM t WHERE id IN (SELECT value FROM json_each(?))`", 1},
		{"hex list", "`SELECT 1 FROM t WHERE id IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))`", 0},
		{"hex list aliased", "`SELECT 1 FROM (SELECT key, CAST(unhex(value) AS TEXT) AS id FROM json_each(?)) AS h`", 0},
		{"numbered parameter", "`SELECT 1 FROM t WHERE id IN (SELECT value FROM json_each(?1))`", 1},
		{"named parameter", "`SELECT 1 FROM t WHERE id IN (SELECT value FROM json_each(:ids))`", 1},
		{"dollar parameter", "`SELECT 1 FROM t WHERE id IN (SELECT value FROM json_each($1))`", 1},
		{"spaced", "`SELECT 1 FROM t WHERE id IN (SELECT value FROM json_each( ? ))`", 1},
		{"unrelated unhex in the literal", "`SELECT unhex(x) FROM t WHERE id IN (SELECT value FROM json_each(?))`", 1},
		{"unhex of another list", "`SELECT 1 FROM t WHERE a IN (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?)) AND b IN (SELECT value FROM json_each(?))`", 1},
		{"two plain statements", "`SELECT value FROM json_each(?)`, `SELECT j.value FROM json_each(?) j`", 2},
		{"receipt lookup form", "`FROM json_each(?) AS lookup JOIN a ON a.event_id = CAST(unhex(json_extract(lookup.value, '$.x')) AS TEXT)`", 0},
		{"labels column, no parameter", "`EXISTS (SELECT 1 FROM json_each(nodes.labels) WHERE json_each.value = ?)`", 0},
		{"no list", "`SELECT 1`", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package main\nvar _ = []string{" + tt.stmt + "}\n"
			parsed, err := parser.ParseFile(token.NewFileSet(), "probe.go", src, 0)
			require.NoError(t, err)
			sites, _ := plainJSONListSites(parsed)
			require.Len(t, sites, tt.want)
		})
	}
}

// TestSyncExchange_HostileEventIDs_UnresolvedCountIsByteExact judges
// countUnresolvedExchange through ExportExchangeSnapshot: an applied id is
// resolved, a quarantined one and an unknown one are not, for every fixture
// the exchange accepts (ids up to 128 bytes).
func TestSyncExchange_HostileEventIDs_UnresolvedCountIsByteExact(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > 128 {
				t.Skip("over the exchange id budget")
			}
			s, _ := hostileStore(t)
			unresolved := func(id string) int64 {
				_, boundary, err := s.ExportExchangeSnapshot(ctx, "MTIX", "test", []string{id}, 1<<20)
				require.NoError(t, err)
				return boundary.Unresolved
			}
			require.Equal(t, int64(1), unresolved(fx.Value), "an unknown id is unresolved")
			hostileQuarantine(t, s, hostileQuarantineRow(t, fx.Value))
			require.Equal(t, int64(1), unresolved(fx.Value), "a quarantined id is unresolved")
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
				return sqlite.RemoveQuarantined(ctx, tx, fx.Value)
			}))
			require.NoError(t, hostileApply(t, s, hostileCreate(t, fx.Value, "MTIX-1", 1)))
			require.Equal(t, int64(0), unresolved(fx.Value), "an applied id is resolved by its exact bytes")
		})
	}
}

// TestQuarantinedCreateBehind_HostileNodeNumbers_MatchByExactBytes judges the
// node-number list of the dependency lookup: a quarantined creation is found
// by the exact number the dependent event names, and an invalid UTF-8 number
// never matches a creation whose number only differs by U+FFFD.
func TestQuarantinedCreateBehind_HostileNodeNumbers_MatchByExactBytes(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the wire cap")
			}
			s, _ := hostileStore(t)
			create := hostileCreate(t, "ev-create", fx.Value, 1)
			createRow := hostileQuarantineRow(t, "ev-create")
			rawEvent, err := json.Marshal(create)
			require.NoError(t, err)
			createRow.RawEvent = string(rawEvent)
			hostileQuarantine(t, s, createRow)

			dependent := hostileCreate(t, "ev-dependent", fx.Value, 2)
			dependent.OpType = model.OpUpdateField
			var found string
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
				var err error
				found, err = sqlite.QuarantinedCreateBehind(ctx, tx, dependent)
				return err
			}))
			// In production only grammar-valid ASCII numbers reach this lookup:
			// the ingest validator refuses every other node id first.
			if validator.ValidateEventNodeIDs(dependent) != nil {
				t.Logf("node id %q is outside the grammar: it never reaches the lookup in production; checked here as a defensive property", fx.Value)
			}
			if utf8.ValidString(fx.Value) {
				require.Equal(t, "ev-create", found, "the creation of the exact number is found")
			} else {
				require.Empty(t, found, "a number that is not valid UTF-8 matches no JSON copy of another number")
			}
		})
	}
}

// TestQuarantinedCreateBehind_NodeIDsReachingIt_AreGrammarValidASCII pins the
// premise stated in QuarantinedCreateBehind's godoc: the ingest validator
// refuses every node id outside the grammar, so only ASCII numbers reach it.
func TestQuarantinedCreateBehind_NodeIDsReachingIt_AreGrammarValidASCII(t *testing.T) {
	for _, fx := range testutil.HostileIDs() {
		e := hostileCreate(t, "ev-x", fx.Value, 1)
		if err := validator.ValidateEventNodeIDs(e); err == nil {
			for i := 0; i < len(fx.Value); i++ {
				require.Less(t, fx.Value[i], byte(0x80), "%s passes the node id grammar, so it must be ASCII", fx.Name)
			}
		}
	}
	require.NoError(t, validator.ValidateEventNodeIDs(hostileCreate(t, "ev-x", "MTIX-1", 1)))
}

// TestSyncStore_MissingLocalEventIDs_HostileIDsMatchByExactBytes judges the
// late-event sweep diff: an applied or quarantined id is found locally, an
// unknown one is missing, for every fixture; the order of the page is kept.
func TestSyncStore_MissingLocalEventIDs_HostileIDsMatchByExactBytes(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the wire cap")
			}
			s, _ := hostileStore(t)
			missing, err := s.MissingLocalEventIDs(ctx, []string{"zz-first", fx.Value, "zz-last"})
			require.NoError(t, err)
			require.Equal(t, []string{"zz-first", fx.Value, "zz-last"}, missing, "unknown ids are missing, in page order")

			require.NoError(t, hostileApply(t, s, hostileCreate(t, fx.Value, "MTIX-1", 1)))
			missing, err = s.MissingLocalEventIDs(ctx, []string{"zz-first", fx.Value, "zz-last"})
			require.NoError(t, err)
			require.Equal(t, []string{"zz-first", "zz-last"}, missing, "an applied id is found by its exact bytes")

			quarantined := fx.Value + "-q"
			hostileQuarantine(t, s, hostileQuarantineRow(t, quarantined))
			missing, err = s.MissingLocalEventIDs(ctx, []string{quarantined, "zz-last"})
			require.NoError(t, err)
			require.Equal(t, []string{"zz-last"}, missing, "a quarantined id is found by its exact bytes")
		})
	}
}

// TestSyncPush_HostileUIDs_PendingCreationWithExplicitUIDStillMakesEventsWait
// covers the second UNION branch of pendingCreationSQL: a pending creation
// whose uid differs from its event id, found through idx_sync_events_uid.
func TestSyncPush_HostileUIDs_PendingCreationWithExplicitUIDStillMakesEventsWait(t *testing.T) {
	ctx := context.Background()
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			if len(fx.Value) > hostileWireLimit {
				t.Skip("over the wire cap")
			}
			s, raw := hostileStore(t)
			create := hostileCreate(t, "ev-create-explicit", "MTIX-1", 1)
			create.UID = fx.Value
			require.NoError(t, hostileApply(t, s, create))
			_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pending' WHERE event_id = ?`, "ev-create-explicit")
			require.NoError(t, err)
			singleID(t, idsIn(t, raw, `SELECT uid FROM nodes WHERE id = 'MTIX-1'`), fx.Value)

			about := hostileCreate(t, "ev-about", "MTIX-1", 2)
			about.OpType = model.OpUpdateField
			waiting, err := s.EventsAwaitingCreation(ctx, []*model.SyncEvent{about})
			require.NoError(t, err)
			_, waits := waiting["ev-about"]
			require.True(t, waits, "the task of the pending creation is found through its explicit uid")
		})
	}
}

// Rule 1b: a test may prefix EXPLAIN QUERY PLAN to the package's own constant
// statement. The real id-list statements keep indexed access: the push hold
// release and the sweep diff probe the event_id primary key; the pending read
// is led by the status index (as it was with a plain list) and never scans.
func TestIDListStatements_UsePrimaryKeyIndexes(t *testing.T) {
	_, raw := hostileStore(t)
	tests := []struct {
		name    string
		explain string
		want    []string
		notWant []string
	}{
		{"ReadPendingEventsByID", "EXPLAIN QUERY PLAN " + sqlite.PendingByIDSQLForTest,
			[]string{"SEARCH sync_events USING INDEX idx_sync_events_status_lamport"}, []string{"SCAN sync_events"}},
		{"ReleasePushHolds", "EXPLAIN QUERY PLAN " + sqlite.ReleasePushHoldsSQLForTest,
			[]string{"SEARCH sync_quarantine USING INDEX sqlite_autoindex_sync_quarantine_1 (event_id=?)"}, []string{"SCAN sync_quarantine"}},
		{"MissingLocalEventIDs", "EXPLAIN QUERY PLAN " + sqlite.MissingLocalIDsSQLForTest,
			[]string{
				"SEARCH s USING COVERING INDEX sqlite_autoindex_sync_events_1 (event_id=?)",
				"SEARCH a USING COVERING INDEX sqlite_autoindex_applied_events_1 (event_id=?)",
				"SEARCH q USING COVERING INDEX sqlite_autoindex_sync_quarantine_1 (event_id=?)",
			}, []string{"SCAN s", "SCAN a", "SCAN q"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := raw.Query(tt.explain, "[]")
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
				details = append(details, detail)
			}
			require.NoError(t, rows.Err())
			plan := strings.Join(details, "\n")
			for _, w := range tt.want {
				require.Contains(t, plan, w, plan)
			}
			for _, n := range tt.notWant {
				require.NotContains(t, plan, n+"\n", plan)
				require.False(t, strings.HasSuffix(plan, n), plan)
			}
		})
	}
}
