// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// ErrSearchPathSchema refuses a change to the hub schema when the first
// schema on the session's search_path is not the schema that holds the
// sync tables: what it would create, tables, functions or triggers, would
// land away from the tables it belongs with (MTIX-95.7).
var ErrSearchPathSchema = errors.New("search_path does not start with the sync tables' schema")

// checkSchemaFirst refuses (ErrSearchPathSchema) when a sync table the
// migrations define resolves, through the search_path, to a schema other
// than current_schema(), the schema the migrations create objects in. A
// hub with no sync table yet passes. The message names both schemas and
// how to put the tables' schema first (MTIX-95.7).
func checkSchemaFirst(ctx context.Context, q queryRower) error {
	tables, err := migrations.Tables()
	if err != nil {
		return fmt.Errorf("sync table list: %w", err)
	}
	var tablesSchema, first string
	// The first sync table that search_path resolves to a schema other
	// than current_schema(), with both schema names quoted server-side.
	err = q.QueryRow(ctx, `
		SELECT pg_catalog.quote_ident(n.nspname),
		       COALESCE(pg_catalog.quote_ident(pg_catalog.current_schema()), '(none)')
		FROM unnest($1::text[]) AS t(name)
		JOIN pg_catalog.pg_class c ON c.oid = pg_catalog.to_regclass(t.name)
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname IS DISTINCT FROM pg_catalog.current_schema()
		ORDER BY t.name
		LIMIT 1`, tables).Scan(&tablesSchema, &first)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check the search_path: %w", err)
	}
	return fmt.Errorf("the sync tables are in schema %s, but the first schema on the search_path is %s; "+
		"set the search_path so that %s comes first (for example ALTER ROLE <owner> SET search_path = %s, public, "+
		"or options=-c search_path=%s in the DSN), then run the command again: %w",
		tablesSchema, first, tablesSchema, tablesSchema, tablesSchema, ErrSearchPathSchema)
}
