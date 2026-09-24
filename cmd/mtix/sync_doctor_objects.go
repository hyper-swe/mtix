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

// hubTriggersName is the doctor's name for the check that the hub holds
// every mtix function and trigger (MTIX-95.7).
const hubTriggersName = "hub-triggers"

// hubTriggersInitFix is the fix for a missing function or trigger: run as
// the table owner, mtix sync init recreates every function and trigger
// the migrations define.
const hubTriggersInitFix = "mtix sync init"

// hubObjectState is what the hub holds of the functions and triggers the
// migrations define (MTIX-95.7).
type hubObjectState struct {
	functions, triggers int      // how many the migrations define
	missingFunctions    []string // function names
	missingTriggers     []string // "trigger on schema.table"
	disabledTriggers    []string // "trigger on schema.table (tgenabled X)"
	enableStatements    []string // the ALTER TABLE statement for each disabled trigger
}

// checkHubObjects verifies that the hub holds every function and trigger
// the embedded migrations define (migrations.Functions and
// migrations.Triggers) and that each trigger is enabled for normal
// sessions (tgenabled 'O'). This is the last step of the restore runbook:
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

// readHubTriggers records in state each of triggers that is missing or
// not enabled for normal sessions, with the statement that enables it.
func readHubTriggers(ctx context.Context, pool *transport.Pool, triggers []migrations.Trigger, state *hubObjectState) error {
	tables := make([]string, 0, len(triggers))
	names := make([]string, 0, len(triggers))
	for _, tr := range triggers {
		tables, names = append(tables, tr.Table), append(names, tr.Name)
	}
	// Each migration-defined trigger, its table resolved through the
	// connecting role's search_path as the CLI's statements resolve it:
	// the table's schema, tgenabled ('' when the trigger or its table is
	// missing) and the statement that enables it, quoted server-side
	// (directive SQL Rule 1a).
	rows, err := pool.Inner().Query(ctx, `
		SELECT g.tbl, g.name, COALESCE(n.nspname::text, ''), COALESCE(t.tgenabled::text, ''),
		       CASE WHEN t.oid IS NULL THEN ''
		            ELSE format('ALTER TABLE %I.%I ENABLE TRIGGER %I;', n.nspname, g.tbl, g.name) END
		FROM unnest($1::text[], $2::text[]) AS g(tbl, name)
		LEFT JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(g.tbl)
		LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_catalog.pg_trigger t
		       ON t.tgrelid = c.oid AND t.tgname = g.name AND NOT t.tgisinternal
		ORDER BY 1, 2`, tables, names)
	if err != nil {
		return fmt.Errorf("read hub triggers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, name, schema, enabled, enable string
		if err := rows.Scan(&table, &name, &schema, &enabled, &enable); err != nil {
			return fmt.Errorf("read hub triggers: %w", err)
		}
		state.record(table, name, schema, enabled, enable)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read hub triggers: %w", err)
	}
	return nil
}

// record files one trigger's catalog state: missing when tgenabled is
// empty, not enabled when it is anything but 'O'.
func (s *hubObjectState) record(table, name, schema, enabled, enable string) {
	where := table
	if schema != "" {
		where = schema + "." + table
	}
	label := name + " on " + where
	switch {
	case enabled == "":
		s.missingTriggers = append(s.missingTriggers, label)
	case enabled != "O":
		s.disabledTriggers = append(s.disabledTriggers, label+" (tgenabled "+enabled+")")
		s.enableStatements = append(s.enableStatements, enable)
	}
}

// gradeHubObjects turns the hub's state into the hub-triggers check: PASS
// when every function and trigger is present and enabled, otherwise a
// WARN by default and a FAIL in strict mode that names each gap and
// carries the fix (MTIX-95.7).
func gradeHubObjects(s hubObjectState, strict bool) DoctorCheck {
	check := DoctorCheck{Name: hubTriggersName, Pass: true}
	if len(s.missingFunctions)+len(s.missingTriggers)+len(s.disabledTriggers) == 0 {
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

// hubObjectsFix returns the fix for s: mtix sync init when a function or
// trigger is missing, the ALTER TABLE statements when a trigger is not
// enabled, and both, in that order, when both apply.
func hubObjectsFix(s hubObjectState) string {
	enable := strings.Join(s.enableStatements, " ")
	switch {
	case len(s.missingFunctions)+len(s.missingTriggers) == 0:
		return enable
	case enable == "":
		return hubTriggersInitFix
	default:
		return hubTriggersInitFix + ", then " + enable
	}
}
