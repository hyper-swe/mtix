// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// schemaCurrentName is the doctor's name for the schema check.
const schemaCurrentName = "schema current"

// schemaState is what the schema current check reads from the hub
// (MTIX-95.1.7): whether sync_projects resolves, which objects of
// migration 017 the hub lacks, whether the connecting role can execute the
// collision recorder, which privileges on sync_node_collisions and its
// sequence it holds beyond the least-privilege list, how many create
// events carry a restore epoch outside 0 to the hub's current one, and the
// owners and schemas the fix names.
type schemaState struct {
	projects            bool           // sync_projects resolves through the search_path
	recorder            bool           // record_restore_collision resolves
	missing             []string       // migration 017's objects the hub lacks
	canRecord           bool           // the role may execute the recorder, or the hub lacks it
	grant               string         // the GRANT that lets the role execute it, quoted server-side
	collisionPrivileges []string       // e.g. "INSERT on sync_node_collisions", held by a role that is not the owner
	revokes             []string       // the REVOKE statements that remove them, quoted server-side
	collisionPaths      []string       // every other path by name, for a role administrator
	stampsOutside       int64          // create events stamped below 0 or above the current epoch
	epoch               int64          // the hub's current restore epoch
	stampFix            string         // the UPDATE that sets each to the current epoch, quoted server-side
	hub                 hubObjectState // the tables' owners and schema, and current_schema()
	// registry is the node-number registry index as pg_index records it
	// (MTIX-95.44).
	registry transport.RegistryIndexState
}

// schemaRow is the catalog row readSchemaState reads.
type schemaRow struct {
	recorder, stampFn, trigger bool
}

// checkSchemaCurrent checks, within syncConnectBudget, that the hub has
// the sync_projects table, resolved through the search_path in whatever
// schema the sync tables are (MTIX-95.7), and migration 017's stamp
// trigger, stamp function and collision recorder, which the connecting
// role can execute; on a hub with the recorder, a connecting role other
// than the table owner must hold neither INSERT on sync_node_collisions
// nor USAGE on its sequence, nor reach them, and every create event's
// restore epoch lies from 0 to the hub's current one (MTIX-95.1.7); and
// that the node-number registry index, when present, is valid and ready,
// failing the check in every mode when it is not (MTIX-95.44). It reports
// whether the sync tables are there, so the checks that read the hub's
// catalog can run.
func checkSchemaCurrent(ctx context.Context, dsn string, opts transport.Options, strict bool) (DoctorCheck, bool) {
	cctx, cancel := context.WithTimeout(ctx, syncConnectBudget)
	defer cancel()
	pool, err := transport.New(cctx, dsn, opts)
	if err != nil {
		return DoctorCheck{Name: schemaCurrentName, Detail: err.Error()}, false
	}
	defer pool.Close()
	state, err := readSchemaState(cctx, pool)
	if err == nil && state.recorder {
		state.collisionPrivileges, state.revokes, state.collisionPaths, err = readCollisionPrivileges(cctx, pool)
	}
	if err == nil && state.recorder {
		state.stampsOutside, state.epoch, state.stampFix, err = readStampRange(cctx, pool)
	}
	if err == nil && state.projects {
		state.registry, err = pool.RegistryIndex(cctx)
	}
	if err != nil {
		return DoctorCheck{Name: schemaCurrentName, Detail: err.Error()}, false
	}
	return gradeSchemaCurrent(state, strict), state.projects
}

// readSchemaState reads the schema check's state in one query
// (MTIX-95.1.7). Every name is resolved through the connecting role's
// search_path, as the CLI's statements resolve it; the stamp trigger counts
// only when it executes the stamp function of the tables' schema, by OID.
func readSchemaState(ctx context.Context, pool *transport.Pool) (schemaState, error) {
	var s schemaState
	var r schemaRow
	h := &s.hub
	var owner, ownerIdent string
	// Whether sync_projects, the recorder and the stamp function resolve;
	// whether the stamp trigger on sync_events executes that schema's stamp
	// function; whether the role may execute the recorder, and the GRANT
	// that allows it, quoted server-side (SQL Rule 1a); the tables' schema
	// and owner, raw and quoted; and current_schema().
	err := pool.Inner().QueryRow(ctx, `
		SELECT pg_catalog.to_regclass('sync_projects') IS NOT NULL,
		       pg_catalog.to_regprocedure($1) IS NOT NULL,
		       pg_catalog.to_regprocedure($2 || '()') IS NOT NULL,
		       COALESCE((SELECT true FROM pg_catalog.pg_trigger t
		                 WHERE t.tgrelid = e.oid AND t.tgname = $3 AND NOT t.tgisinternal
		                   AND t.tgfoid = pg_catalog.to_regprocedure(
		                       pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident($2) || '()')), false),
		       COALESCE(pg_catalog.has_function_privilege(pg_catalog.to_regprocedure($1), 'EXECUTE'), true),
		       COALESCE((SELECT pg_catalog.format('GRANT EXECUTE ON FUNCTION %I.%I(%s) TO %I;', pn.nspname,
		                        p.proname, pg_catalog.oidvectortypes(p.proargtypes), current_user)
		                 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace pn ON pn.oid = p.pronamespace
		                 WHERE p.oid = pg_catalog.to_regprocedure($1)), ''),
		       COALESCE(n.nspname::text, ''), COALESCE(pg_catalog.quote_ident(n.nspname), ''),
		       COALESCE(pg_catalog.pg_get_userbyid(e.relowner)::text, ''),
		       COALESCE(pg_catalog.quote_ident(pg_catalog.pg_get_userbyid(e.relowner)::text), ''),
		       COALESCE(pg_catalog.current_schema()::text, '')
		FROM (SELECT 1) AS one
		LEFT JOIN pg_catalog.pg_class e ON e.oid = pg_catalog.to_regclass('sync_events')
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = e.relnamespace`,
		migrations.RecordCollisionSignature, migrations.StampFunction, migrations.StampTrigger,
	).Scan(&s.projects, &r.recorder, &r.stampFn, &r.trigger, &s.canRecord, &s.grant,
		&h.tablesSchema, &h.tablesSchemaIdent, &owner, &ownerIdent, &h.currentSchema)
	if err != nil {
		return schemaState{}, fmt.Errorf("read the hub schema: %w", err)
	}
	if owner != "" {
		h.owners, h.ownerIdents = []string{owner}, []string{ownerIdent}
	}
	s.recorder = r.recorder
	s.missing = missing017(r, h.tablesSchema)
	return s, nil
}

// missing017 names each object of migration 017 the hub lacks, in the
// order the migration creates them (MTIX-95.1.7).
func missing017(r schemaRow, schema string) []string {
	where := "sync_events"
	if schema != "" {
		where = schema + ".sync_events"
	}
	var out []string
	if !r.stampFn {
		out = append(out, "function "+migrations.StampFunction+"()")
	}
	if !r.recorder {
		out = append(out, "function "+migrations.RecordCollisionSignature)
	}
	if !r.trigger {
		out = append(out, "trigger "+migrations.StampTrigger+" on "+where)
	}
	return out
}

// gradeSchemaCurrent turns the state into the schema current check
// (MTIX-95.1.7): FAIL without sync_projects, as before; PASS when the hub
// has migration 017, the role can execute the recorder and holds no
// privilege on sync_node_collisions or its sequence beyond the
// least-privilege list, and no create event is stamped outside 0 to the
// hub's current epoch; otherwise a WARN by default and a FAIL in strict
// mode, like the hub-triggers check, naming each gap, with the fix: the
// table owner's steps, then the paths a role administrator removes, by
// name. A registry index that is not valid or not ready is a FAIL in
// every mode: it checks no new create (MTIX-95.44).
func gradeSchemaCurrent(s schemaState, strict bool) DoctorCheck {
	check := DoctorCheck{Name: schemaCurrentName}
	if !s.projects {
		check.Detail = "sync_projects table missing \u2014 run 'mtix sync init'"
		return check
	}
	check.Pass = true
	gaps, steps := schemaGaps(s)
	if len(gaps) == 0 {
		check.Detail = "ok"
		return check
	}
	var parts []string
	switch {
	case strict:
		check.Pass = false
		parts = append(parts, "strict mode (sync.keep_roles is set)")
	case s.registry.NotUsable():
		check.Pass = false
	default:
		check.Warn = true
	}
	parts = append(parts, gaps...)
	var fix []string
	if len(steps) > 0 {
		fix = append(fix, tableOwnerPrefix(s.hub.owners)+strings.Join(steps, ", then "))
	}
	if paths := collisionPathsToName(s); len(paths) > 0 {
		fix = append(fix, collisionPathsIntro+strings.Join(paths, "; "))
		parts = append(parts, "run each part of the fix as the role it names, then run mtix sync doctor again")
	} else {
		parts = append(parts, "run the fix as the table owner, then run mtix sync doctor again")
	}
	check.Detail = strings.Join(parts, "; ")
	check.Fix = strings.Join(fix, ", then ")
	return check
}

// schemaGaps describes each gap of s and returns the fix's steps in order
// (MTIX-95.1.7): mtix sync init, after the search_path step when init would
// refuse, for a hub without migration 017, where pushes keep working; the
// printed GRANT for a role that cannot execute the recorder, until which a
// push that meets a restore collision fails; for the privileges the
// least-privilege list does not name, removed once every syncing client is
// upgraded, the table owner's printed REVOKE statements; for create events
// stamped outside 0 to the hub's current epoch, the owner's printed
// UPDATE; for a registry index that is not valid or not ready, mtix sync
// migrate --yes (MTIX-95.44); the paths a role administrator removes are
// the last part of the fix (gradeSchemaCurrent).
func schemaGaps(s schemaState) (gaps, steps []string) {
	if len(s.missing) > 0 {
		gap := "the hub schema predates migration 017, with which the hub stamps every event's " +
			"restore epoch and records restore collisions itself; missing: " + strings.Join(s.missing, ", ")
		if s.canRecord {
			gap += "; pushes keep working"
		}
		gaps = append(gaps, gap)
		if s.hub.searchPathMismatch() {
			steps = append(steps, s.hub.searchPathStep())
		}
		steps = append(steps, hubTriggersInitFix)
	}
	if !s.canRecord {
		gaps = append(gaps, "the connecting role cannot execute record_restore_collision, which records "+
			"restore collisions on the hub; a push that meets a restore collision fails until the table owner "+
			"runs the printed GRANT")
		steps = append(steps, s.grant)
	}
	if len(s.collisionPrivileges) > 0 {
		gaps = append(gaps, "the connecting role holds or can reach "+strings.Join(s.collisionPrivileges, " and ")+
			", which the least-privilege list does not name (restore collisions are recorded through "+
			"record_restore_collision); once every syncing client is upgraded, the table owner runs the "+
			"printed REVOKE and a role administrator removes each named path")
		if len(s.revokes) > 0 {
			steps = append(steps, strings.Join(s.revokes, " "))
		}
	}
	if s.stampsOutside > 0 {
		gaps = append(gaps, fmt.Sprintf("create events stamped with a restore epoch outside 0 to %d, the hub's "+
			"current epoch: %d; restore-collision checks treat each as not earlier than the current epoch, and "+
			"the table owner sets each to the current epoch with the printed UPDATE", s.epoch, s.stampsOutside))
		steps = append(steps, s.stampFix)
	}
	if gap, step := registryIndexGap(s.registry); gap != "" {
		gaps, steps = append(gaps, gap), append(steps, step)
	}
	return gaps, steps
}

// collisionPathsIntro introduces the paths a role administrator removes
// (MTIX-95.1.7).
const collisionPathsIntro = `a role administrator removes each of these paths ` +
	`(see "Hub health checks" in the user manual): `

// collisionPathsToName returns the paths of s a role administrator removes:
// its named paths, or, when the role can write collision rows and neither
// a REVOKE nor a path was found, each privilege itself, so the fix is
// never empty (MTIX-95.1.7).
func collisionPathsToName(s schemaState) []string {
	if len(s.collisionPrivileges) == 0 {
		return nil
	}
	if len(s.collisionPaths) > 0 || len(s.revokes) > 0 {
		return s.collisionPaths
	}
	paths := make([]string, 0, len(s.collisionPrivileges))
	for _, p := range s.collisionPrivileges {
		paths = append(paths, unnamedPath(p))
	}
	return paths
}

// tableOwnerPrefix introduces a fix run as the table owner, named when
// known.
func tableOwnerPrefix(owners []string) string {
	if len(owners) == 0 {
		return "as the table owner: "
	}
	return "as the table owner (" + strings.Join(owners, ", ") + "): "
}
