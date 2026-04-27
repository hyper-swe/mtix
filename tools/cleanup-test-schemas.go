// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build cleanup

// cleanup-test-schemas.go is the nuclear-option cleanup tool for orphaned
// test schemas left behind by E2E test failures. Build tag `cleanup`
// excludes it from normal compilation.
//
// Usage:
//
//	go run -tags=cleanup ./tools/cleanup-test-schemas.go \
//	    -dsn "$MTIX_TEST_SUPABASE_DSN" \
//	    -older-than 24h \
//	    -dry-run=false
//
// The tool drops every schema matching `mtix_test_*` whose creation
// timestamp (encoded in the schema name) is older than -older-than. The
// default is dry-run; pass -dry-run=false to actually drop.
//
// SAFETY: this tool only ever issues `DROP SCHEMA "mtix_test_*" CASCADE`.
// Schemas not matching the prefix are NEVER touched. The prefix check
// is performed both client-side (here) and via the LIKE pattern below.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// connectFn is the SQL-driver entry point. We do not import a driver here
// because doing so requires a `// +build cleanup` matching driver build tag
// which complicates `go mod tidy`. Operators wire in pgx or lib/pq via the
// driver-import side-effect pattern in their own short Go file:
//
//	package main
//	import _ "github.com/jackc/pgx/v5/stdlib" // import-by-side-effect
//	func main() { CleanupMain() }
//
// CleanupMain is exported for that purpose. The default in-package main()
// uses sql.Open with the driver name "postgres" which is registered by
// the standard pq driver if linked.
//
//nolint:gochecknoglobals // CLI entry point
var connectFn = sql.Open

func main() {
	if err := CleanupMain(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

// CleanupMain is the testable entry point. Returning errors instead of
// log.Fatal'ing inside the body lets us unit-test argument parsing.
func CleanupMain(args []string) error {
	fs := flag.NewFlagSet("cleanup-test-schemas", flag.ContinueOnError)
	dsn := fs.String("dsn", os.Getenv("MTIX_CLEANUP_DSN"),
		"postgres DSN (or set MTIX_CLEANUP_DSN). NEVER passed as a command-line arg in CI.")
	driver := fs.String("driver", "postgres", "registered sql driver name")
	olderThan := fs.Duration("older-than", 24*time.Hour,
		"only drop schemas older than this duration (encoded in the schema name)")
	dryRun := fs.Bool("dry-run", true, "if true, only print the schemas that would be dropped")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("dsn is required (--dsn or MTIX_CLEANUP_DSN)")
	}

	db, err := connectFn(*driver, *dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	candidates, err := listOrphanSchemas(ctx, db, *olderThan)
	if err != nil {
		return fmt.Errorf("list schemas: %w", err)
	}
	if len(candidates) == 0 {
		log.Println("no orphan schemas found")
		return nil
	}

	for _, schema := range candidates {
		if *dryRun {
			log.Printf("DRY-RUN would drop schema: %s", schema)
			continue
		}
		if err := dropSchema(ctx, db, schema); err != nil {
			log.Printf("WARN failed to drop %s: %v", schema, err)
			continue
		}
		log.Printf("dropped schema: %s", schema)
	}
	return nil
}

// listOrphanSchemas queries information_schema for matching schemas, parses
// the timestamp from the name, and returns those older than threshold.
//
// Naming convention (see provider.go uniqueSchemaName): mtix_test_<unix>_<rand>
// or mtix_test_<tag>_<unix>_<rand>. We tolerate both shapes.
func listOrphanSchemas(ctx context.Context, db *sql.DB, threshold time.Duration) ([]string, error) {
	const q = `SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'mtix_test_%'`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	cutoff := time.Now().Add(-threshold)
	var orphans []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		ts, ok := timestampFromSchemaName(name)
		if !ok {
			// Unexpected naming; skip rather than guess.
			continue
		}
		if ts.Before(cutoff) {
			orphans = append(orphans, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return orphans, nil
}

// timestampFromSchemaName extracts the unix timestamp embedded in a
// generated schema name. Returns (zero, false) if the name does not match
// either of the two documented shapes.
func timestampFromSchemaName(name string) (time.Time, bool) {
	parts := strings.Split(name, "_")
	// Expected: ["mtix", "test", "<tag>"?, "<unix>", "<rand>"]
	// Walk from the right, find the first all-digit segment that is at
	// least 10 chars (unix epoch second precision is 10 digits as of 2001).
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		if len(p) < 10 {
			continue
		}
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			continue
		}
		// Sanity: reject implausible timestamps (before 2020 or after 2100).
		if v < 1577836800 || v > 4102444800 {
			continue
		}
		return time.Unix(v, 0), true
	}
	return time.Time{}, false
}

// dropSchema runs DROP SCHEMA ... CASCADE with the schema name embedded
// directly. We validate the name matches the expected prefix before
// substitution; postgres does not support parameterised identifiers.
func dropSchema(ctx context.Context, db *sql.DB, schema string) error {
	if !strings.HasPrefix(schema, "mtix_test_") {
		return fmt.Errorf("refusing to drop schema not matching mtix_test_ prefix: %s", schema)
	}
	// Quote the identifier defensively even though our own naming uses only
	// safe characters.
	quoted := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	_, err := db.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	return err
}
