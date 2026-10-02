// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// recorderSignature is the identity signature of the hub's collision
// recorder (MTIX-95.1.7).
const recorderSignature = "record_restore_collision(text, text, text, text, bigint)"

// openInSchema opens a transport pool on the fixture database acting as
// role, with schema alone on its search_path.
func (f *hubFixture) openInSchema(role, schema string) *transport.Pool {
	f.t.Helper()
	u := f.dbURL
	q := u.Query()
	q.Set("options", "-c role="+role+" -c search_path="+schema)
	u.RawQuery = q.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, u.String(), transport.Options{InsecureTLS: true})
	require.NoError(f.t, err)
	f.t.Cleanup(pool.Close)
	return pool
}

// recorderState lists, for each function migration 017 creates in schema,
// whether it runs as its owner, its owner, its settings and whether PUBLIC
// may execute it.
func recorderState(f *hubFixture, schema string) []string {
	return f.queryStrings(`
		SELECT concat_ws(' ', p.proname::text, p.prosecdef::text, pg_catalog.pg_get_userbyid(p.proowner)::text,
		       pg_catalog.array_to_string(p.proconfig, ';'),
		       pg_catalog.has_function_privilege('public', p.oid, 'EXECUTE')::text)
		FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname IN ('hub_stamp_restore_epoch', 'record_restore_collision')
		ORDER BY 1`, schema)
}

// TestMigration017_HubInOtherSchema_FunctionsRunAsOwnerWithFixedSearchPath:
// migrated into a schema other than public, both functions of migration
// 017 run as the table owner with the search_path fixed to that schema and
// pg_temp last, and PUBLIC cannot execute them; the stamp trigger on
// sync_events executes the stamp function of that schema. A second migrate
// keeps the functions' privileges and the trigger itself (MTIX-95.1.7).
func TestMigration017_HubInOtherSchema_FunctionsRunAsOwnerWithFixedSearchPath(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", "hub_x", owner)
	pool := f.openInSchema(owner, "hub_x")
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	require.Equal(t, []string{
		"hub_stamp_restore_epoch true " + owner + " search_path=hub_x, pg_temp false",
		"record_restore_collision true " + owner + " search_path=hub_x, pg_temp false",
	}, recorderState(f, "hub_x"))
	stampTrigger := `SELECT t.oid::text || ' ' || p.oid::regprocedure::text FROM pg_catalog.pg_trigger t
		JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
		WHERE t.tgrelid = 'hub_x.sync_events'::regclass AND t.tgname = 'sync_events_stamp_restore_epoch'`
	before := f.queryStrings(stampTrigger)
	require.Len(t, before, 1)
	require.True(t, strings.HasSuffix(before[0], " hub_x.hub_stamp_restore_epoch()"), before[0])
	acl := `SELECT p.proname::text || ' ' || COALESCE(p.proacl::text, '-') FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'hub_x' ORDER BY 1`
	aclBefore := f.queryStrings(acl)

	require.NoError(t, pool.Migrate(ctx))
	require.Equal(t, before, f.queryStrings(stampTrigger), "a second migrate keeps the trigger")
	require.Equal(t, aclBefore, f.queryStrings(acl), "a second migrate changes no function privilege")
}

// TestMigration017_OwnerDefaultPrivileges_NewFunctionsWithoutPublicExecute:
// when the table owner's default privileges grant EXECUTE on new functions
// in the hub's schema to a role, migration 017 still creates both of its
// functions without EXECUTE for PUBLIC, and that role keeps its grant. A
// second migrate changes no privilege of the existing functions, not even
// an EXECUTE the owner has since granted to PUBLIC (MTIX-95.1.7).
func TestMigration017_OwnerDefaultPrivileges_NewFunctionsWithoutPublicExecute(t *testing.T) {
	f := newHubFixture(t)
	owner, caller := f.ownerRole(), f.role("caller")
	f.ddl("CREATE SCHEMA %I AUTHORIZATION %I", "hub_d", owner)
	f.ddl("ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I GRANT EXECUTE ON FUNCTIONS TO %I", owner, "hub_d", caller)
	pool := f.openInSchema(owner, "hub_d")
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	require.Equal(t, []string{
		"hub_stamp_restore_epoch true " + owner + " search_path=hub_d, pg_temp false",
		"record_restore_collision true " + owner + " search_path=hub_d, pg_temp false",
	}, recorderState(f, "hub_d"), "PUBLIC cannot execute either function")
	require.Equal(t, []string{"true"}, f.queryStrings(`SELECT pg_catalog.has_function_privilege($1,
		'hub_d.`+recorderSignature+`'::regprocedure, 'EXECUTE')::text`, caller), "the default privilege's grantee keeps it")

	f.ddl("GRANT EXECUTE ON FUNCTION %I."+recorderSignature+" TO PUBLIC", "hub_d")
	acl := `SELECT p.proname::text || ' ' || COALESCE(p.proacl::text, '-') FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'hub_d' ORDER BY 1`
	aclBefore := f.queryStrings(acl)
	require.NoError(t, pool.Migrate(ctx))
	require.Equal(t, aclBefore, f.queryStrings(acl), "a second migrate changes no function privilege")
}

// TestHubStamp_RoleWithInsertOnly_StoredWithHubEpoch: a role that may only
// INSERT into sync_events, with no privilege on sync_hub_state, inserts
// rows, each with a client-supplied restore epoch; each is stored with the
// hub's epoch, before and after mark-restored (MTIX-95.1.7).
func TestHubStamp_RoleWithInsertOnly_StoredWithHubEpoch(t *testing.T) {
	f := newHubFixture(t)
	owner, writer := f.ownerRole(), f.role("writer")
	ownerPool := f.openAs(owner, transport.Options{})
	ctx := context.Background()
	require.NoError(t, ownerPool.Migrate(ctx))
	f.ddl("GRANT INSERT ON public.sync_events TO %I", writer)
	writerPool := f.openAs(writer, transport.Options{})

	insert := func(id string, supplied any) {
		_, err := writerPool.Inner().Exec(ctx, `INSERT INTO sync_events (event_id, project_prefix, node_id, op_type,
			payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, restore_epoch)
			VALUES ($1, 'MTIX', $1, 'comment', '{"body":"x"}', 1, 1, '{"w":1}', 'w', '0123456789abcdef', $2)`, id, supplied)
		require.NoError(t, err)
	}
	insert("e-before", int64(-3))
	_, err := ownerPool.MarkRestored(ctx)
	require.NoError(t, err)
	insert("e-after-low", int64(0))
	insert("e-after-high", int64(9))
	insert("e-after-null", nil)
	require.Equal(t, []string{"e-after-high 1", "e-after-low 1", "e-after-null 1", "e-before 0"}, f.queryStrings(
		`SELECT event_id || ' ' || restore_epoch::text FROM public.sync_events ORDER BY 1`))
}

// crossEpochPush pushes a held create, runs mark-restored and pushes a
// distinct create for the same number, whose uid differs from its event
// id, returning the second push's outcome.
func crossEpochPush(t *testing.T, pool *transport.Pool, suffix string) (*model.SyncEvent, []transport.RestoreCollision, error) {
	t.Helper()
	ctx := context.Background()
	held := uidEvent("0193fa00-0000-7000-8000-0000000b"+suffix+"001", "MTIX-1.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)
	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)
	incoming := uidEvent("0193fa00-0000-7000-8000-0000000b"+suffix+"002", "MTIX-1.4", "bob", 2)
	incoming.UID = "0193fa00-0000-7000-8000-0000000b" + suffix + "0aa"
	_, _, _, collisions, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{incoming})
	return incoming, collisions, err
}

// TestPush_CrossEpochCreate_RecordsItsUID: a cross-epoch create whose uid
// differs from its event id is recorded by the hub's recorder with that
// uid as incoming_uid, next to its event id (MTIX-95.1.7).
func TestPush_CrossEpochCreate_RecordsItsUID(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	pool := f.openAs(owner, transport.Options{})
	require.NoError(t, pool.Migrate(context.Background()))

	incoming, collisions, err := crossEpochPush(t, pool, "3")
	require.NoError(t, err)
	require.Len(t, collisions, 1)
	require.NotEqual(t, incoming.EventID, incoming.UID)
	require.Equal(t, []string{incoming.EventID + " " + incoming.UID}, f.queryStrings(
		`SELECT incoming_event_id || ' ' || incoming_uid FROM public.sync_node_collisions`))
}

// TestPush_HubWithoutMigration017_StillStampsAndRecords: on a hub that
// lacks migration 017's trigger and functions (not yet initialized since
// the upgrade), pushes keep working: each create is stored with the epoch
// the push read from the hub, and a cross-epoch create is still recorded
// as a restore collision, with its own uid (MTIX-95.1.7).
func TestPush_HubWithoutMigration017_StillStampsAndRecords(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	pool := f.openAs(owner, transport.Options{})
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	f.exec(`DROP TRIGGER IF EXISTS sync_events_stamp_restore_epoch ON public.sync_events`)
	f.exec(`DROP FUNCTION IF EXISTS public.hub_stamp_restore_epoch()`)
	f.exec(`DROP FUNCTION IF EXISTS public.` + recorderSignature)

	incoming, collisions, err := crossEpochPush(t, pool, "1")
	require.NoError(t, err, "a hub without migration 017 still accepts the push")
	require.Len(t, collisions, 1)
	require.Equal(t, []string{"open 0 1 " + incoming.UID}, f.queryStrings(
		`SELECT status || ' ' || held_epoch::text || ' ' || detected_epoch::text || ' ' || incoming_uid
		 FROM public.sync_node_collisions WHERE incoming_event_id = $1`, incoming.EventID))
	later := uidEvent("0193fa00-0000-7000-8000-0000000b1003", "MTIX-1.5", "bob", 3)
	_, _, _, _, err = pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{later})
	require.NoError(t, err)
	require.Equal(t, int64(1), stampedEpoch(t, pool, later.EventID), "stored with the epoch the push read")
}

// TestPush_RecorderReportsNothingOnRecord_FailsPush: when the hub's
// recorder reports that no collision for a blocked create is on record,
// the push fails, names the create, and lands nothing, so a create is
// never withheld without a collision row for an administrator to resolve
// (MTIX-95.1.7).
func TestPush_RecorderReportsNothingOnRecord_FailsPush(t *testing.T) {
	f := newHubFixture(t)
	owner := f.ownerRole()
	pool := f.openAs(owner, transport.Options{})
	require.NoError(t, pool.Migrate(context.Background()))
	f.exec(`CREATE OR REPLACE FUNCTION public.record_restore_collision(p_project_prefix text,
		p_display_path text, p_incoming_event_id text, p_incoming_uid text, p_incoming_wall_clock_ts bigint)
		RETURNS boolean LANGUAGE sql AS 'SELECT false'`)

	incoming, collisions, err := crossEpochPush(t, pool, "2")
	require.Error(t, err, "a blocked create with no collision on record fails the push")
	require.Contains(t, err.Error(), incoming.EventID)
	require.Contains(t, err.Error(), "not on record")
	require.Empty(t, collisions)
	require.Equal(t, []string{"0"}, f.queryStrings(`SELECT count(*)::text FROM public.sync_node_collisions`))
}

// TestHarden_RecorderExecute_RevokedBySignatureKeptRoleKeeps: mtix sync
// harden --apply revokes EXECUTE on record_restore_collision from a role
// that is not kept, naming the function by its signature, and a kept
// syncing role keeps it; verification then passes (MTIX-95.1.7).
func TestHarden_RecorderExecute_RevokedBySignatureKeptRoleKeeps(t *testing.T) {
	f := newHubFixture(t)
	owner, team, api := f.ownerRole(), f.role("team"), f.role("api")
	warnings := transport.NewWarningLog()
	pool := f.openAs(owner, transport.Options{OnNotice: warnings.Record})
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	f.ddl("GRANT EXECUTE ON FUNCTION public."+recorderSignature+" TO %I, %I", team, api)

	result, err := pool.Harden(ctx, transport.HardenRequest{Apply: true, KeepRoles: []string{team}, Warnings: warnings})
	require.NoError(t, err)
	require.Contains(t, result.Executed,
		"REVOKE ALL ON FUNCTION public."+recorderSignature+" FROM "+api+" CASCADE")
	require.True(t, result.After.Clean(), "%+v", result.After.Findings)
	can := `SELECT pg_catalog.has_function_privilege($1::text, 'public.` + recorderSignature + `', 'EXECUTE')::text`
	require.Equal(t, []string{"false"}, f.queryStrings(can, api))
	require.Equal(t, []string{"true"}, f.queryStrings(can, team), "the kept role keeps EXECUTE")
}
