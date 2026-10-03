// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	syncpkg "github.com/hyper-swe/mtix/internal/sync"
)

// registryIndexName is the node-number registry index migration 009
// declares.
const registryIndexName = "sync_events_node_registry_uidx"

// registryIndexState reads pg_index for the registry index on sync_events:
// whether it exists and, if so, its indisvalid and indisready flags
// (MTIX-95.44).
func registryIndexState(t *testing.T, db *pgxpool.Pool) (present, valid, ready bool) {
	t.Helper()
	err := db.QueryRow(context.Background(), `
		SELECT i.indisvalid, i.indisready
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = 'sync_events'::regclass AND c.relname = $1`,
		registryIndexName).Scan(&valid, &ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, false
	}
	require.NoError(t, err)
	return true, valid, ready
}

// registryIndexDef returns the registry index's definition as
// pg_get_indexdef prints it.
func registryIndexDef(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var def string
	require.NoError(t, db.QueryRow(context.Background(), `
		SELECT pg_catalog.pg_get_indexdef(c.oid)
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_index i ON i.indexrelid = c.oid
		WHERE i.indrelid = 'sync_events'::regclass AND c.relname = $1`,
		registryIndexName).Scan(&def))
	return def
}

// eventsDigest is one digest over every sync_events row, so a test proves
// that the sweep and the index build leave every row exactly as it was.
func eventsDigest(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var digest string
	require.NoError(t, db.QueryRow(context.Background(), `
		SELECT md5(COALESCE(string_agg(e::text, ',' ORDER BY e.event_id), ''))
		FROM sync_events e`).Scan(&digest))
	return digest
}

// openGate registers a remap-aware client for each project, so each
// project's version gate is open.
func openGate(t *testing.T, pool *transport.Pool, projects ...string) {
	t.Helper()
	for _, p := range projects {
		require.NoError(t, pool.UpsertProjectClient(context.Background(),
			p, "aaaaaaaaaaaaaaaa", syncpkg.UIDKeyedMinVersion))
	}
}

// insertRawCreate inserts one create row directly, as a test inserts the
// duplicates a hub may hold from before the registry index existed; a
// unique violation is returned, anything else fails the test.
func insertRawCreate(t *testing.T, db *pgxpool.Pool, eventID, prefix, nodeID string) error {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash)
		VALUES ($1, $2, $3, $1, 'create_node', '{"title":"x"}',
		        1, 1, '{"alice":1}', 'alice', '0123456789abcdef')`,
		eventID, prefix, nodeID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return err
	}
	require.NoError(t, err)
	return nil
}

// requireSuperuserEnv is set to "1" on the self-hosted PG jobs, where the
// test role is a superuser and PostgreSQL enforces its own limits: there a
// skipped or accepted-instead-of-refused case is a failure, never a quiet
// pass (MTIX-95.55). It is unset against a managed server.
const requireSuperuserEnv = "MTIX_PG_TEST_REQUIRE_SUPERUSER"

// setRegistryIndexFlags sets indisvalid and indisready of the registry
// index in pg_index, as the superuser the test DSN names, to reproduce
// each state an interrupted or failed build leaves (MTIX-95.44). A role
// that is not a superuser cannot write pg_catalog (a managed server), so
// the calling test is skipped there, never weakened (MTIX-95.55).
func setRegistryIndexFlags(t *testing.T, db *pgxpool.Pool, valid, ready bool) {
	t.Helper()
	var super bool
	require.NoError(t, db.QueryRow(context.Background(),
		`SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname = current_user`).Scan(&super))
	if !super && os.Getenv(requireSuperuserEnv) == "1" {
		t.Fatal("the self-hosted PG job must connect as a superuser")
	}
	if !super {
		t.Skip("needs a superuser to simulate an interrupted index build; " +
			"runs on the self-hosted PG16/17 CI jobs")
	}
	tag, err := db.Exec(context.Background(), `
		UPDATE pg_catalog.pg_index SET indisvalid = $1, indisready = $2
		WHERE indrelid = 'sync_events'::regclass
		  AND indexrelid = (SELECT c.oid FROM pg_catalog.pg_class c
		                    JOIN pg_catalog.pg_index i ON i.indexrelid = c.oid
		                    WHERE i.indrelid = 'sync_events'::regclass AND c.relname = $3)`,
		valid, ready, registryIndexName)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected(), "the registry index exists")
}

// failedRegistryBuild leaves the registry index as a failed CONCURRENTLY
// build leaves it: two creates share a number, the build fails with a
// unique violation, and the two creates are removed again, so only the
// index that is not valid remains.
func failedRegistryBuild(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := db.Exec(ctx, `DROP INDEX IF EXISTS sync_events_node_registry_uidx`)
	require.NoError(t, err)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544f1", "TMP", "TMP-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544f2", "TMP", "TMP-1", "", 2)
	_, err = db.Exec(ctx, `CREATE UNIQUE INDEX CONCURRENTLY sync_events_node_registry_uidx
		ON sync_events (project_prefix, node_id) WHERE op_type = 'create_node'`)
	require.Error(t, err, "the build over two creates of one number fails")
	_, err = db.Exec(ctx, `DELETE FROM sync_events WHERE project_prefix = 'TMP'`)
	require.NoError(t, err)
	present, valid, _ := registryIndexState(t, db)
	require.True(t, present, "the failed build leaves its index behind")
	require.False(t, valid)
}

// TestSweepDuplicates_DuplicatesInSeveralProjects_RecordsEveryLoser: the
// registry index covers the whole hub, so the sweep run for one project
// records the losers of every project, each with its own project, in the
// remap ledger and as a conflict, and changes no event row (MTIX-95.44).
func TestSweepDuplicates_DuplicatesInSeveralProjects_RecordsEveryLoser(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "LEG", "LEG-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a2", "LEG", "LEG-1", "", 2)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544b1", "MTIX", "MTIX-1", "", 3)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544b2", "MTIX", "MTIX-1", "", 4)
	before := eventsDigest(t, db)

	rep, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 2, rep.Resolved, "a loser in each project")
	require.Equal(t, 1, countRemaps(t, db, "LEG"), "the other project's loser is recorded under its project")
	require.Equal(t, 1, countRemaps(t, db, "MTIX"))
	require.Equal(t, 2, countConflicts(t, db))
	require.Equal(t, before, eventsDigest(t, db), "the sweep changes no event row")
}

// TestPreviewDuplicates_DuplicatesInSeveralProjects_CountsEveryLoser: the
// dry run counts what --yes would record, in every project (MTIX-95.44).
func TestPreviewDuplicates_DuplicatesInSeveralProjects_CountsEveryLoser(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "LEG", "LEG-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a2", "LEG", "LEG-1", "", 2)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544b1", "MTIX", "MTIX-1", "", 3)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544b2", "MTIX", "MTIX-1", "", 4)

	n, err := pool.PreviewDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, 0, countRemaps(t, db, "LEG"), "the preview records nothing")
}

// leftOutCase is a hub state whose duplicate creates the registry index
// build leaves out: the creates to insert, in order ({event_id, project,
// number, uid}), the event ids the index leaves out, the numbers the
// index then guards, and the remaps the sweep records per project.
type leftOutCase struct {
	name    string
	creates [][4]string
	leftOut []string
	guarded [][2]string
	remaps  map[string]int
}

// leftOutCases are the duplicate shapes a hub may hold (MTIX-95.44).
func leftOutCases() []leftOutCase {
	id := func(s string) string { return "0193fa00-0000-7000-8000-000000" + s }
	return []leftOutCase{
		{"distinct nodes in two projects",
			[][4]string{{id("9544a1"), "LEG", "LEG-1", ""}, {id("9544a2"), "LEG", "LEG-1", ""},
				{id("9544b1"), "MTIX", "MTIX-1", ""}, {id("9544b2"), "MTIX", "MTIX-1", ""}},
			[]string{id("9544a2"), id("9544b2")}, [][2]string{{"LEG", "LEG-1"}, {"MTIX", "MTIX-1"}},
			map[string]int{"LEG": 1, "MTIX": 1}},
		{"three creates of one number",
			[][4]string{{id("9544c1"), "MTIX", "MTIX-7", ""}, {id("9544c2"), "MTIX", "MTIX-7", ""},
				{id("9544c3"), "MTIX", "MTIX-7", ""}},
			[]string{id("9544c2"), id("9544c3")}, [][2]string{{"MTIX", "MTIX-7"}}, map[string]int{"MTIX": 2}},
		{"the winner's node created twice",
			[][4]string{{id("9544d1"), "MTIX", "MTIX-2", id("9544d1")}, {id("9544d2"), "MTIX", "MTIX-2", id("9544d1")}},
			[]string{id("9544d2")}, [][2]string{{"MTIX", "MTIX-2"}}, map[string]int{"MTIX": 0}},
		{"a loser node created twice",
			[][4]string{{id("9544e1"), "MTIX", "MTIX-3", id("9544e1")}, {id("9544e2"), "MTIX", "MTIX-3", id("9544e2")},
				{id("9544e3"), "MTIX", "MTIX-3", id("9544e2")}},
			[]string{id("9544e2"), id("9544e3")}, [][2]string{{"MTIX", "MTIX-3"}}, map[string]int{"MTIX": 1}},
	}
}

// TestEnsureRegistryIndex_SweptDuplicates_BuildsValidIndexLeavingThemOut:
// with the version gate open, after the sweep, the build succeeds on a hub
// whose projects hold duplicate creates. The index is valid and ready,
// its definition names each create it leaves out, no event row changed,
// every loser node is recorded, and the index refuses a new create of
// every contested number while a new number is accepted (MTIX-95.44).
func TestEnsureRegistryIndex_SweptDuplicates_BuildsValidIndexLeavingThemOut(t *testing.T) {
	for _, tc := range leftOutCases() {
		t.Run(tc.name, func(t *testing.T) {
			db := migratedPoolWithoutIndex(t)
			pool := poolFor(t, db)
			for i, c := range tc.creates {
				insertCreate(t, db, c[0], c[1], c[2], c[3], int64(i+1))
			}
			openGate(t, pool, "MTIX")
			before := eventsDigest(t, db)

			_, err := pool.SweepDuplicates(context.Background(), "MTIX")
			require.NoError(t, err)
			res, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
			require.NoError(t, err, "the build succeeds over the recorded duplicates")
			require.True(t, res.Added)

			present, valid, ready := registryIndexState(t, db)
			require.Equal(t, [3]bool{true, true, true}, [3]bool{present, valid, ready})
			def := registryIndexDef(t, db)
			for _, id := range tc.leftOut {
				require.Contains(t, def, id, "the definition names each create it leaves out")
			}
			require.Equal(t, before, eventsDigest(t, db), "no event row is deleted or changed")
			for project, n := range tc.remaps {
				require.Equal(t, n, countRemaps(t, db, project), "each loser node is recorded")
			}
			for i, g := range tc.guarded {
				fresh := "0193fa00-0000-7000-8000-0000009544" + string(rune('0'+i)) + "0"
				require.Error(t, insertRawCreate(t, db, fresh, g[0], g[1]),
					"the index refuses a new create of a contested number")
			}
			require.NoError(t, insertRawCreate(t, db, "0193fa00-0000-7000-8000-00000095449f", "MTIX", "MTIX-99"))
		})
	}
}

// TestEnsureRegistryIndex_UnrecordedDuplicates_RefusesAndLeavesNoIndex:
// with the version gate open, duplicate creates the sweep has not recorded
// make the build refuse before it starts, naming the command that records
// them, and no index is left behind (MTIX-95.44).
func TestEnsureRegistryIndex_UnrecordedDuplicates_RefusesAndLeavesNoIndex(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "LEG", "LEG-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a2", "LEG", "LEG-1", "", 2)
	openGate(t, pool, "MTIX")

	_, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.Error(t, err)
	require.Contains(t, err.Error(), "mtix sync migrate --yes", "the refusal names the command that records them")
	present, _, _ := registryIndexState(t, db)
	require.False(t, present, "no index is left behind")
}

// TestEnsureRegistryIndex_IndexNotValidOrNotReady_DropsAndBuildsAgain:
// with the version gate open, an index of the registry's name that is not
// valid or not ready is dropped and built again, valid and ready, and the
// result reports it added (MTIX-95.44).
func TestEnsureRegistryIndex_IndexNotValidOrNotReady_DropsAndBuildsAgain(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, db *pgxpool.Pool)
	}{
		{"a failed concurrent build", failedRegistryBuild},
		{"not valid, not ready", func(t *testing.T, db *pgxpool.Pool) { setRegistryIndexFlags(t, db, false, false) }},
		{"ready, not valid", func(t *testing.T, db *pgxpool.Pool) { setRegistryIndexFlags(t, db, false, true) }},
		{"valid, not ready", func(t *testing.T, db *pgxpool.Pool) { setRegistryIndexFlags(t, db, true, false) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := openTestPool(t)
			require.NoError(t, pool.Migrate(context.Background()))
			db := pool.Inner()
			insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "MTIX", "MTIX-1", "", 1)
			openGate(t, pool, "MTIX")
			tc.setup(t, db)

			res, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
			require.NoError(t, err)
			require.True(t, res.Added, "an index that is not valid or not ready is built again")
			present, valid, ready := registryIndexState(t, db)
			require.Equal(t, [3]bool{true, true, true}, [3]bool{present, valid, ready})
		})
	}
}

// TestEnsureRegistryIndex_IndexNotValidAndGateClosed_LeavesItForLater:
// while the version gate is closed nothing is built or dropped; the index
// that is not valid stays for the run that finds the gate open
// (MTIX-95.44).
func TestEnsureRegistryIndex_IndexNotValidAndGateClosed_LeavesItForLater(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	db := pool.Inner()
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "MTIX", "MTIX-1", "", 1)
	setRegistryIndexFlags(t, db, false, false)

	res, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.NoError(t, err)
	require.False(t, res.GateOpen)
	require.False(t, res.Added)
	present, valid, _ := registryIndexState(t, db)
	require.True(t, present)
	require.False(t, valid)
}

// aboveCapPairs is a number of duplicate pairs well above the number of
// creates the registry index can leave out (MTIX-95.44).
const aboveCapPairs = 2000

// TestEnsureRegistryIndex_LeftOutCreatesAboveTheCap_RefusesAndLeavesNoIndex:
// with the version gate open, a hub whose recorded duplicate creates are
// more than the index can leave out is refused before the build, with the
// count, the cap, that pushes keep working and the ticket tracking the
// durable design; no index is left behind (MTIX-95.44).
func TestEnsureRegistryIndex_LeftOutCreatesAboveTheCap_RefusesAndLeavesNoIndex(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertDuplicatePairs(t, db, aboveCapPairs)
	openGate(t, pool, "MTIX")
	_, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)

	_, err = pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.Error(t, err)
	for _, want := range []string{"2000 duplicate creates", "pushes keep working", "MTIX-97.12"} {
		require.Contains(t, err.Error(), want)
	}
	present, _, _ := registryIndexState(t, db)
	require.False(t, present, "no index is left behind")
}

// insertDuplicatePairs gives project MTIX pairs creates of each of pairs
// numbers, with random event ids, the least compressible form.
func insertDuplicatePairs(t *testing.T, db *pgxpool.Pool, pairs int) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
		SELECT gen_random_uuid()::text, 'MTIX', 'MTIX-' || (g % $1), NULL, 'create_node', '{"title":"x"}',
		       1, g, '{"alice":1}', 'alice', '0123456789abcdef'
		FROM generate_series(1, 2 * $1) AS g`, pairs)
	require.NoError(t, err)
}

// TestEnsureRegistryIndex_LeftOutCreatesAtTheCap_BuildsValidIndex: a hub
// with exactly as many recorded duplicate creates as the cap, each with a
// random event id, gets a valid and ready index: the cap fits in the
// index's catalog row (MTIX-95.44).
func TestEnsureRegistryIndex_LeftOutCreatesAtTheCap_BuildsValidIndex(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertDuplicatePairs(t, db, transport.MaxRegistryLeftOut)
	openGate(t, pool, "MTIX")
	_, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)

	res, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, transport.MaxRegistryLeftOut, res.LeftOut)
	present, valid, ready := registryIndexState(t, db)
	require.Equal(t, [3]bool{true, true, true}, [3]bool{present, valid, ready})
}

// TestSweepDuplicates_ValidIndexLeavingDuplicatesOut_IsNoOp: once the
// registry index is valid and ready, every duplicate it leaves out was
// recorded when it was built, so the sweep and the preview do nothing
// (MTIX-95.44).
func TestSweepDuplicates_ValidIndexLeavingDuplicatesOut_IsNoOp(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "LEG", "LEG-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a2", "LEG", "LEG-1", "", 2)
	openGate(t, pool, "LEG")
	_, err := pool.SweepDuplicates(context.Background(), "LEG")
	require.NoError(t, err)
	_, err = pool.EnsureRegistryIndex(context.Background(), "LEG")
	require.NoError(t, err)
	// Without its remap row, a sweep that scanned would record the loser
	// again; the valid index makes it skip the scan.
	_, err = db.Exec(context.Background(), `DELETE FROM node_renumber_remaps`)
	require.NoError(t, err)

	rep, err := pool.SweepDuplicates(context.Background(), "LEG")
	require.NoError(t, err)
	require.Equal(t, 0, rep.Resolved)
	require.Equal(t, 0, countRemaps(t, db, "LEG"), "the sweep records nothing")
	n, err := pool.PreviewDuplicates(context.Background(), "LEG")
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

// winnerCase is one push against a number whose left-out duplicate stays
// on the hub: the event, and the registered create a renumber names, or ""
// when the push must not renumber.
type winnerCase struct {
	name       string
	event      *model.SyncEvent
	registered string
}

// TestPushEventsWithRenumbers_HubKeepsLeftOutDuplicate_RegistryAnswersWithTheWinner:
// on a hub that keeps a left-out duplicate create next to its registry
// index, the push-side registry lookup answers with the sweep's winner,
// the lowest event id: the winner's create pushed again and a new create
// of the winner's node are not renumbered, and a create of another node
// is renumbered against the winner (MTIX-95.44).
func TestPushEventsWithRenumbers_HubKeepsLeftOutDuplicate_RegistryAnswersWithTheWinner(t *testing.T) {
	const (
		winner = "0193fa00-0000-7000-8000-0000009544b1"
		loser  = "0193fa00-0000-7000-8000-0000009544b2"
	)
	event := func(id, uid string) *model.SyncEvent {
		e := makeEvent(id, "MTIX-1.4", "alice", 5)
		e.UID = uid
		return e
	}
	cases := []winnerCase{
		{"the winner's create pushed again", event(winner, winner), ""},
		{"a new create of the winner's node", event("0193fa00-0000-7000-8000-0000009544b3", winner), ""},
		{"a create of another node", event("0193fa00-0000-7000-8000-0000009544b4", "0193fa00-0000-7000-8000-0000009544b4"), winner},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := migratedPoolWithoutIndex(t)
			pool := poolFor(t, db)
			// The loser row is written first, so the heap holds it first.
			insertCreate(t, db, loser, "MTIX", "MTIX-1.4", loser, 2)
			insertCreate(t, db, winner, "MTIX", "MTIX-1.4", winner, 1)
			_, err := db.Exec(context.Background(), `CREATE UNIQUE INDEX sync_events_node_registry_uidx
				ON sync_events (project_prefix, node_id)
				WHERE op_type = 'create_node' AND event_id <> ALL ('{`+loser+`}'::text[])`)
			require.NoError(t, err)

			_, _, renumbers, err := pool.PushEventsWithRenumbers(context.Background(), []*model.SyncEvent{tc.event})
			require.NoError(t, err)
			if tc.registered == "" {
				require.Empty(t, renumbers)
				return
			}
			require.Len(t, renumbers, 1)
			require.Equal(t, tc.registered, renumbers[0].RegisteredEventID, "the renumber names the winner")
		})
	}
}

// TestBuildRegistryIndex_BuildFails_LeavesNoIndexAndRestoresTimeout: a
// build that fails part way, here over two creates of one number it was
// not told to leave out, drops what it left behind, names the fix, and
// sets the connection's statement_timeout back (MTIX-95.44).
func TestBuildRegistryIndex_BuildFails_LeavesNoIndexAndRestoresTimeout(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a1", "MTIX", "MTIX-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544a2", "MTIX", "MTIX-1", "", 2)

	timeout, err := transport.BuildRegistryIndexForTest(context.Background(), pool, []string{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not create unique index")
	require.Contains(t, err.Error(), "mtix sync migrate --yes")
	present, _, _ := registryIndexState(t, db)
	require.False(t, present, "the failed build leaves no index behind")
	require.Equal(t, "10s", timeout, "the pool's statement_timeout is set back")
}

// insertLongIDPairs gives project MTIX two creates of each of pairs
// numbers, each with a random event id of idLen hex characters.
func insertLongIDPairs(t *testing.T, db *pgxpool.Pool, pairs, idLen int) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash)
		SELECT substr(repeat(md5(random()::text), 1 + $2 / 32), 1, $2), 'MTIX', 'MTIX-' || (g % $1), NULL,
		       'create_node', '{"title":"x"}', 1, g, '{"alice":1}', 'alice', '0123456789abcdef'
		FROM generate_series(1, 2 * $1) AS g`, pairs, idLen)
	require.NoError(t, err)
}

// requireCapRefusal fails unless err is the refusal of a hub whose
// duplicate creates are more than the index can leave out.
func requireCapRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	for _, want := range []string{"more than the registry index can leave out", "pushes keep working", "MTIX-97.12"} {
		require.Contains(t, err.Error(), want)
	}
}

// TestEnsureRegistryIndex_CapBoundary_BuildsAtTheCapRefusesAboveIt: both
// limits are inclusive: exactly MaxRegistryLeftOut left-out creates, and
// exactly MaxRegistryLeftOutBytes of their event ids, build a valid index;
// one create more, or exactly one byte more, is refused before the build
// (MTIX-95.44).
func TestEnsureRegistryIndex_CapBoundary_BuildsAtTheCapRefusesAboveIt(t *testing.T) {
	cases := []struct {
		name         string
		pairs, idLen int
		wantRefusal  bool
	}{
		{"count at the cap", transport.MaxRegistryLeftOut, 20, false},
		{"count one above the cap", transport.MaxRegistryLeftOut + 1, 20, true},
		{"bytes at the limit", 12, transport.MaxRegistryLeftOutBytes / 12, false},
		{"bytes one above the limit", 13, (transport.MaxRegistryLeftOutBytes + 1) / 13, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := migratedPoolWithoutIndex(t)
			pool := poolFor(t, db)
			insertLongIDPairs(t, db, tc.pairs, tc.idLen)
			openGate(t, pool, "MTIX")
			_, err := pool.SweepDuplicates(context.Background(), "MTIX")
			require.NoError(t, err)

			_, err = pool.EnsureRegistryIndex(context.Background(), "MTIX")
			present, valid, _ := registryIndexState(t, db)
			if !tc.wantRefusal {
				require.NoError(t, err)
				require.True(t, present && valid)
				return
			}
			requireCapRefusal(t, err)
			require.False(t, present)
		})
	}
}

// TestEnsureRegistryIndex_NotValidIndexAboveTheCap_RefusesAndKeepsTheIndex:
// a refusal changes nothing: on a hub whose registry index is not valid and
// whose recorded duplicates are above the cap, the index that is not valid
// stays, so mtix sync doctor keeps reporting it (MTIX-95.44).
func TestEnsureRegistryIndex_NotValidIndexAboveTheCap_RefusesAndKeepsTheIndex(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertDuplicatePairs(t, db, transport.MaxRegistryLeftOut+1)
	_, err := db.Exec(context.Background(), `CREATE UNIQUE INDEX CONCURRENTLY sync_events_node_registry_uidx
		ON sync_events (project_prefix, node_id) WHERE op_type = 'create_node'`)
	require.Error(t, err, "the failed build leaves an index that is not valid")
	openGate(t, pool, "MTIX")
	_, err = pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)

	_, err = pool.EnsureRegistryIndex(context.Background(), "MTIX")
	requireCapRefusal(t, err)
	present, valid, _ := registryIndexState(t, db)
	require.True(t, present, "the refusal drops nothing")
	require.False(t, valid)
}

// TestEnsureRegistryIndex_LongEventIDsAboveTheByteLimit_RefusesAndLeavesNoIndex:
// the index definition is bounded by bytes, so long event ids are refused
// before the build even below the count cap (MTIX-95.44).
func TestEnsureRegistryIndex_LongEventIDsAboveTheByteLimit_RefusesAndLeavesNoIndex(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	insertLongIDPairs(t, db, 40, 256)
	openGate(t, pool, "MTIX")
	_, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)

	_, err = pool.EnsureRegistryIndex(context.Background(), "MTIX")
	requireCapRefusal(t, err)
	present, _, _ := registryIndexState(t, db)
	require.False(t, present)
}

// TestBuildRegistryIndex_DefinitionTooLarge_RefusesWithTheCapMessage: a
// build whose definition PostgreSQL cannot store (SQLSTATE 54000) is
// refused with the same exact message as the cap (MTIX-95.44).
func TestBuildRegistryIndex_DefinitionTooLarge_RefusesWithTheCapMessage(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	var ids []string
	require.NoError(t, db.QueryRow(context.Background(), `
		SELECT array_agg(substr(repeat(md5(random()::text), 9), 1, 256)) FROM generate_series(1, 200)`).Scan(&ids))

	_, err := transport.BuildRegistryIndexForTest(context.Background(), pool, ids)
	if err == nil {
		// A server that stores a very large index predicate (a managed
		// server) accepts the build; the index must then be a good one.
		// Self-hosted PostgreSQL refuses, so there accepting is a failure.
		require.NotEqual(t, "1", os.Getenv(requireSuperuserEnv),
			"self-hosted PostgreSQL must refuse the definition (SQLSTATE 54000)")
		present, valid, ready := registryIndexState(t, db)
		require.Equal(t, [3]bool{true, true, true}, [3]bool{present, valid, ready})
		var predLen int
		require.NoError(t, db.QueryRow(context.Background(), `
			SELECT length(pg_get_expr(i.indpred, i.indrelid)) FROM pg_catalog.pg_index i
			JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
			WHERE i.indrelid = 'sync_events'::regclass AND c.relname = $1`,
			registryIndexName).Scan(&predLen))
		require.Greater(t, predLen, 40000, "the server stored the large predicate it was sent")
		t.Log("the server accepted the large index definition; the cap refusal is not reachable here")
		return
	}
	requireCapRefusal(t, err)
	require.NotContains(t, err.Error(), "run mtix sync migrate --yes again")
	present, _, _ := registryIndexState(t, db)
	require.False(t, present)
}

// TestEnsureRegistryIndex_LoserNodeAtTwoNumbers_RefusesNamingTheCreates: the
// remap ledger holds one row per node, so a node that lost two numbers is
// recorded at one only; the build refuses, naming the create it cannot
// record, instead of leaving it out unrecorded (MTIX-95.44).
func TestEnsureRegistryIndex_LoserNodeAtTwoNumbers_RefusesNamingTheCreates(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	const node = "0193fa00-0000-7000-8000-0000009544c9"
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544c1", "LEG", "LEG-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544c2", "LEG", "LEG-1", node, 2)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544c3", "LEG", "LEG-2", "", 3)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544c4", "LEG", "LEG-2", node, 4)
	openGate(t, pool, "LEG")
	_, err := pool.SweepDuplicates(context.Background(), "LEG")
	require.NoError(t, err)

	_, err = pool.EnsureRegistryIndex(context.Background(), "LEG")
	require.Error(t, err)
	require.Contains(t, err.Error(), "recorded at another number")
	require.Contains(t, err.Error(), "0193fa00-0000-7000-8000-0000009544c4")
	present, _, _ := registryIndexState(t, db)
	require.False(t, present)
}

// TestEnsureRegistryIndex_LoserRecordedInAnotherProject_Refuses: a node's
// remap row is for another project at the same number, so the create it
// lost in this project is not recorded: the build refuses, naming it
// (MTIX-95.44).
func TestEnsureRegistryIndex_LoserRecordedInAnotherProject_Refuses(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	const node = "0193fa00-0000-7000-8000-0000009544d9"
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544d1", "AAA", "X-1", "", 1)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544d2", "AAA", "X-1", node, 2)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544d3", "BBB", "X-1", "", 3)
	insertCreate(t, db, "0193fa00-0000-7000-8000-0000009544d4", "BBB", "X-1", node, 4)
	openGate(t, pool, "AAA")
	_, err := pool.SweepDuplicates(context.Background(), "AAA")
	require.NoError(t, err)

	_, err = pool.EnsureRegistryIndex(context.Background(), "AAA")
	require.Error(t, err)
	require.Contains(t, err.Error(), "recorded at another number")
	require.Contains(t, err.Error(), "0193fa00-0000-7000-8000-0000009544d4")
	present, _, _ := registryIndexState(t, db)
	require.False(t, present)
}
