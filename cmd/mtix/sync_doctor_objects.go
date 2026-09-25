// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// hubTriggersName is the doctor's name for the check that the hub holds
// every mtix function and trigger (MTIX-95.7).
const hubTriggersName = "hub-triggers"

// hubTriggersInitFix is the fix for a missing function or trigger and for
// a trigger bound to another function: run as the table owner, mtix sync
// init recreates every function and trigger the migrations define, each
// bound to its function, in one transaction (MTIX-95.7).
const hubTriggersInitFix = "mtix sync init"

// hubObjectState is what the hub holds of the functions and triggers the
// migrations define (MTIX-95.7).
type hubObjectState struct {
	functions, triggers int      // how many the migrations define
	missingFunctions    []string // function names
	missingTriggers     []string // "trigger on schema.table"
	wrongFunction       []string // "trigger on schema.table calls f, not g"
	disabledTriggers    []string // "trigger on schema.table (tgenabled X)"
	enableStatements    []string // the ALTER TABLE statement for each disabled trigger
	owners              []string // the owners of the triggers' tables, who run the fix
}

// triggerRow is the catalog state of one trigger the migrations define:
// its table and name, the table's schema and owner ("" when the table is
// missing), tgenabled ("" when the trigger is missing), the function it
// executes, the function its migration binds, and the statement that
// enables it, quoted server-side (MTIX-95.7).
type triggerRow struct {
	table, name, schema, owner, enabled string
	function, wantFunction              string
	enable                              string
}

// checkHubObjects verifies that the hub holds every function and trigger
// the embedded migrations define (migrations.Functions and
// migrations.Triggers), that each trigger executes the function its
// migration binds, and that each is enabled (tgenabled 'O', or 'A' for a
// trigger that fires always). This is the last step of the restore runbook:
// a dump holds the tables only, and mtix sync init, run as the table
// owner, recreates the functions and triggers (F-39, MTIX-95.7).
//
// Like the hub-privileges check, a gap is a WARN by default and a FAIL in
// strict mode (sync.keep_roles set), and so is a check that cannot run.
// The hub is contacted only here, while the doctor runs.
func checkHubObjects(ctx context.Context, dsn string, hubReady bool, opts transport.Options) DoctorCheck {
	kept, err := doctorKeptRoles()
	if err != nil {
		return DoctorCheck{Name: hubTriggersName, Detail: err.Error()}
	}
	strict := len(kept) > 0
	if !hubReady {
		return unverifiedCheck(hubTriggersName, strict, "skipped (hub unreachable or schema not current); "+
			"fix the checks above, then run mtix sync doctor again")
	}
	state, err := readHubObjects(ctx, dsn, opts)
	if err != nil {
		return unverifiedCheck(hubTriggersName, strict, "could not read the hub's functions and triggers: "+
			err.Error()+"; check the hub connection, then run mtix sync doctor again")
	}
	return gradeHubObjects(state, strict)
}

// readHubObjects connects within the sync connect budget and reads which
// of the migrations' functions and triggers the hub lacks or has not
// enabled (MTIX-95.7).
func readHubObjects(ctx context.Context, dsn string, opts transport.Options) (hubObjectState, error) {
	functions, err := migrations.Functions()
	if err != nil {
		return hubObjectState{}, fmt.Errorf("function list: %w", err)
	}
	triggers, err := migrations.Triggers()
	if err != nil {
		return hubObjectState{}, fmt.Errorf("trigger list: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, syncConnectBudget)
	defer cancel()
	pool, err := transport.New(cctx, dsn, opts)
	if err != nil {
		return hubObjectState{}, fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	state := hubObjectState{functions: len(functions), triggers: len(triggers)}
	if state.missingFunctions, err = missingHubFunctions(cctx, pool, functions); err != nil {
		return hubObjectState{}, err
	}
	if err := readHubTriggers(cctx, pool, triggers, &state); err != nil {
		return hubObjectState{}, err
	}
	return state, nil
}

// missingHubFunctions returns, sorted, each of functions that the
// connecting role's search_path does not resolve. Every mtix function
// takes no arguments.
func missingHubFunctions(ctx context.Context, pool *transport.Pool, functions []string) ([]string, error) {
	// Each migration-defined function that does not resolve as name().
	rows, err := pool.Inner().Query(ctx, `
		SELECT f.name FROM unnest($1::text[]) AS f(name)
		WHERE pg_catalog.to_regprocedure(f.name || '()') IS NULL
		ORDER BY f.name`, functions)
	if err != nil {
		return nil, fmt.Errorf("read hub functions: %w", err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read hub functions: %w", err)
		}
		missing = append(missing, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read hub functions: %w", err)
	}
	return missing, nil
}

// readHubTriggers records in state each of triggers that is missing,
// executes another function than its migration binds, or is not enabled,
// with the statement that enables it and the owner of its table.
func readHubTriggers(ctx context.Context, pool *transport.Pool, triggers []migrations.Trigger, state *hubObjectState) error {
	tables := make([]string, 0, len(triggers))
	names := make([]string, 0, len(triggers))
	functions := make([]string, 0, len(triggers))
	for _, tr := range triggers {
		tables, names, functions = append(tables, tr.Table), append(names, tr.Name), append(functions, tr.Function)
	}
	// Each migration-defined trigger, its table resolved through the
	// connecting role's search_path as the CLI's statements resolve it:
	// the table's schema and owner, tgenabled ('' when the trigger or its
	// table is missing), the function the trigger executes next to the one
	// its migration binds, and the statement that enables it, quoted
	// server-side (directive SQL Rule 1a).
	rows, err := pool.Inner().Query(ctx, `
		SELECT g.tbl, g.name, COALESCE(n.nspname::text, ''),
		       COALESCE(pg_catalog.pg_get_userbyid(c.relowner)::text, ''),
		       COALESCE(t.tgenabled::text, ''), COALESCE(p.proname::text, ''), g.fn,
		       CASE WHEN t.oid IS NULL THEN ''
		            ELSE format('ALTER TABLE %I.%I ENABLE TRIGGER %I;', n.nspname, g.tbl, g.name) END
		FROM unnest($1::text[], $2::text[], $3::text[]) AS g(tbl, name, fn)
		LEFT JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(g.tbl)
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_trigger t
		       ON t.tgrelid = c.oid AND t.tgname = g.name AND NOT t.tgisinternal
		LEFT JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
		ORDER BY 1, 2`, tables, names, functions)
	if err != nil {
		return fmt.Errorf("read hub triggers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r triggerRow
		if err := rows.Scan(&r.table, &r.name, &r.schema, &r.owner, &r.enabled,
			&r.function, &r.wantFunction, &r.enable); err != nil {
			return fmt.Errorf("read hub triggers: %w", err)
		}
		state.record(r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read hub triggers: %w", err)
	}
	return nil
}

// record files one trigger's catalog state: missing when tgenabled is
// empty; calling the wrong function when it executes another function than
// its migration binds (recreating it also enables it); not enabled when
// tgenabled is anything but 'O' (fires in normal sessions) or 'A' (fires
// always), the two states mtix sync harden accepts. It also records the
// owner of the trigger's table, who runs the fix (MTIX-95.7).
func (s *hubObjectState) record(r triggerRow) {
	if r.owner != "" && !slices.Contains(s.owners, r.owner) {
		s.owners = append(s.owners, r.owner)
	}
	where := r.table
	if r.schema != "" {
		where = r.schema + "." + r.table
	}
	label := r.name + " on " + where
	switch {
	case r.enabled == "":
		s.missingTriggers = append(s.missingTriggers, label)
	case r.function != r.wantFunction:
		s.wrongFunction = append(s.wrongFunction, label+" calls "+r.function+", not "+r.wantFunction)
	case r.enabled != "O" && r.enabled != "A":
		s.disabledTriggers = append(s.disabledTriggers, label+" (tgenabled "+r.enabled+")")
		s.enableStatements = append(s.enableStatements, r.enable)
	}
}

// gradeHubObjects turns the hub's state into the hub-triggers check: PASS
// when every function and trigger is present and enabled, otherwise a
// WARN by default and a FAIL in strict mode that names each gap and
// carries the fix (MTIX-95.7).
func gradeHubObjects(s hubObjectState, strict bool) DoctorCheck {
	check := DoctorCheck{Name: hubTriggersName, Pass: true}
	if len(s.missingFunctions)+len(s.missingTriggers)+len(s.wrongFunction)+len(s.disabledTriggers) == 0 {
		check.Detail = fmt.Sprintf("every mtix function (%d) and trigger (%d) is present and enabled",
			s.functions, s.triggers)
		return check
	}
	var parts []string
	if strict {
		check.Pass = false
		parts = append(parts, "strict mode (sync.keep_roles is set)")
	} else {
		check.Warn = true
	}
	for _, gap := range []struct {
		label string
		items []string
	}{
		{"missing functions: ", s.missingFunctions},
		{"missing triggers: ", s.missingTriggers},
		{"triggers calling another function: ", s.wrongFunction},
		{"triggers not enabled: ", s.disabledTriggers},
	} {
		if len(gap.items) > 0 {
			parts = append(parts, gap.label+strings.Join(gap.items, ", "))
		}
	}
	parts = append(parts, "the append-only tables are not fully protected; "+
		"run the fix as the table owner, then run mtix sync doctor again")
	check.Detail = strings.Join(parts, "; ")
	check.Fix = hubObjectsFix(s)
	return check
}

// hubObjectsFix returns the fix for s, naming the table owner who runs
// it, with its steps joined by ", then ": mtix sync init when a function
// or trigger is missing or a trigger executes another function (init
// replaces it inside its transaction, with no separate drop), and the
// ALTER TABLE statement for each trigger that is not enabled (MTIX-95.7).
func hubObjectsFix(s hubObjectState) string {
	var steps []string
	if len(s.missingFunctions)+len(s.missingTriggers)+len(s.wrongFunction) > 0 {
		steps = append(steps, hubTriggersInitFix)
	}
	if len(s.enableStatements) > 0 {
		steps = append(steps, strings.Join(s.enableStatements, " "))
	}
	if len(steps) == 0 {
		return ""
	}
	owner := "as the table owner: "
	if len(s.owners) > 0 {
		owner = "as the table owner (" + strings.Join(s.owners, ", ") + "): "
	}
	return owner + strings.Join(steps, ", then ")
}
