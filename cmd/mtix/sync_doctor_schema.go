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
// collision recorder, and the owners and schemas the fix names.
type schemaState struct {
	projects  bool           // sync_projects resolves through the search_path
	missing   []string       // migration 017's objects the hub lacks
	canRecord bool           // the role may execute the recorder, or the hub lacks it
	grant     string         // the GRANT that lets the role execute it, quoted server-side
	hub       hubObjectState // the tables' owners and schema, and current_schema()
}

// schemaRow is the catalog row readSchemaState reads.
type schemaRow struct {
	recorder, stampFn, trigger bool
}

// checkSchemaCurrent checks, within syncConnectBudget, that the hub has
// the sync_projects table, resolved through the search_path in whatever
// schema the sync tables are (MTIX-95.7), and migration 017's stamp
// trigger, stamp function and collision recorder, which the connecting
// role can execute (MTIX-95.1.7). It reports whether the sync tables are
// there, so the checks that read the hub's catalog can run.
func checkSchemaCurrent(ctx context.Context, dsn string, opts transport.Options, strict bool) (DoctorCheck, bool) {
	cctx, cancel := context.WithTimeout(ctx, syncConnectBudget)
	defer cancel()
	pool, err := transport.New(cctx, dsn, opts)
	if err != nil {
		return DoctorCheck{Name: schemaCurrentName, Detail: err.Error()}, false
	}
	defer pool.Close()
	state, err := readSchemaState(cctx, pool)
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
// has migration 017 and the role can execute the recorder; otherwise a
// WARN by default and a FAIL in strict mode, like the hub-triggers check,
// naming each gap, with the fix the table owner runs: mtix sync init
// (after the search_path step when init would refuse), then the GRANT.
func gradeSchemaCurrent(s schemaState, strict bool) DoctorCheck {
	check := DoctorCheck{Name: schemaCurrentName}
	if !s.projects {
		check.Detail = "sync_projects table missing — run 'mtix sync init'"
		return check
	}
	check.Pass = true
	if len(s.missing) == 0 && s.canRecord {
		check.Detail = "ok"
		return check
	}
	var parts, steps []string
	if strict {
		check.Pass = false
		parts = append(parts, "strict mode (sync.keep_roles is set)")
	} else {
		check.Warn = true
	}
	if len(s.missing) > 0 {
		parts = append(parts, "the hub schema predates migration 017, with which the hub stamps every event's "+
			"restore epoch and records restore collisions itself; missing: "+strings.Join(s.missing, ", ")+
			"; pushes keep working")
		if s.hub.searchPathMismatch() {
			steps = append(steps, s.hub.searchPathStep())
		}
		steps = append(steps, hubTriggersInitFix)
	}
	if !s.canRecord {
		parts = append(parts, "the connecting role cannot execute record_restore_collision, which records "+
			"restore collisions on the hub; a push that meets a restore collision fails until the table owner grants it")
		steps = append(steps, s.grant)
	}
	parts = append(parts, "run the fix as the table owner, then run mtix sync doctor again")
	check.Detail = strings.Join(parts, "; ")
	check.Fix = tableOwnerPrefix(s.hub.owners) + strings.Join(steps, ", then ")
	return check
}

// tableOwnerPrefix introduces a fix run as the table owner, named when
// known.
func tableOwnerPrefix(owners []string) string {
	if len(owners) == 0 {
		return "as the table owner: "
	}
	return "as the table owner (" + strings.Join(owners, ", ") + "): "
}
