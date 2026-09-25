// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// ErrSearchPathSchema refuses a change to the hub schema when the first
// schema on the session's search_path is not the schema that holds the
// sync tables: what it would create, tables, functions or triggers, would
// land away from the tables it belongs with (MTIX-95.7).
var ErrSearchPathSchema = errors.New("search_path does not start with the sync tables' schema")

// checkSchemaFirst refuses (ErrSearchPathSchema) when the search_path
// reaches an mtix hub in a schema other than current_schema(), the schema
// the migrations create objects in: sync_projects and sync_events both
// resolve to that same other schema, and it holds an mtix function. An
// unrelated table that only shares a sync table's name, and a hub with no
// sync table yet, pass. The message names both schemas and how to put the
// hub's schema first (MTIX-95.7).
func checkSchemaFirst(ctx context.Context, q queryRower) error {
	functions, err := migrations.Functions()
	if err != nil {
		return fmt.Errorf("mtix function list: %w", err)
	}
	var tablesSchema, first, example string
	// The schema sync_projects and sync_events both resolve to, when it is
	// not current_schema() and holds an mtix function; both schema names
	// quoted server-side, and the search_path to suggest: that schema
	// alone when it is public, else that schema and then public.
	err = q.QueryRow(ctx, `
		SELECT pg_catalog.quote_ident(n.nspname),
		       COALESCE(pg_catalog.quote_ident(pg_catalog.current_schema()), '(none)'),
		       CASE WHEN n.nspname = 'public' THEN 'public'
		            ELSE pg_catalog.quote_ident(n.nspname) || ', public' END
		FROM pg_catalog.pg_class p
		JOIN pg_catalog.pg_class e ON e.relnamespace = p.relnamespace
		JOIN pg_catalog.pg_namespace n ON n.oid = p.relnamespace
		WHERE p.oid = pg_catalog.to_regclass('sync_projects')
		  AND e.oid = pg_catalog.to_regclass('sync_events')
		  AND n.nspname IS DISTINCT FROM pg_catalog.current_schema()
		  AND EXISTS (SELECT 1 FROM pg_catalog.pg_proc f
		              WHERE f.pronamespace = n.oid AND f.proname = ANY($1::text[]))`,
		functions).Scan(&tablesSchema, &first, &example)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check the search_path: %w", err)
	}
	return fmt.Errorf("the sync tables are in schema %s, but the first schema on the search_path is %s; "+
		"set the search_path so that %s comes first (for example %q, or %q in the DSN), then run the command again: %w",
		tablesSchema, first, tablesSchema, "ALTER ROLE <owner> SET search_path = "+example,
		"options=-c search_path="+strings.ReplaceAll(example, " ", ""), ErrSearchPathSchema)
}
