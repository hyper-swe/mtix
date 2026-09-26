// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// RegistryIndexName is the node-number registry index migration 009
// declares: the partial unique index over create_node rows of sync_events
// (ADR-003 §6).
const RegistryIndexName = "sync_events_node_registry_uidx"

// MaxRegistryLeftOut is the most duplicate creates the registry index
// leaves out (MTIX-95.44). The index names each create it leaves out in
// its predicate, and pg_index, which has no TOAST table, keeps that
// predicate in one catalog row of at most 8160 bytes: measured on
// PostgreSQL 16 and 17, the row holds 217 random event ids (253 of the
// time-ordered form mtix mints) before the build fails with "row is too
// big". The cap is about half of that. A hub with more is refused before
// the build; MTIX-97.12 tracks a design without this limit.
const MaxRegistryLeftOut = 100

// MaxRegistryLeftOutBytes bounds the left-out event ids by their total
// length, since the catalog row's limit is in bytes: 100 ids of the
// 36-character form mtix mints, about half of the 217 that fit
// (MTIX-95.44). A list must meet both limits.
const MaxRegistryLeftOutBytes = 3600

// registryCleanupBudget bounds the drop of an index a failed build left
// behind, which runs even when the caller's context has ended.
const registryCleanupBudget = 30 * time.Second

// RegistryIndexState is the registry index as pg_index records it
// (MTIX-95.44). An index that is not ready checks no new create; one that
// is ready but not valid still refuses a duplicate create, but queries do
// not use it and it must be built again. CREATE INDEX IF NOT EXISTS skips
// either.
type RegistryIndexState struct {
	// Present reports whether sync_events has an index of the registry's
	// name.
	Present bool `json:"present"`
	// Valid and Ready are pg_index.indisvalid and pg_index.indisready.
	Valid bool `json:"valid"`
	Ready bool `json:"ready"`
	// Schema is the schema that holds the index, and SchemaIdent the
	// same name quoted server-side, for a fix that names the index.
	Schema      string `json:"-"`
	SchemaIdent string `json:"-"`
}

// Usable reports whether the index is present, valid and ready: the one
// state in which it checks every new create (MTIX-95.44).
func (s RegistryIndexState) Usable() bool {
	return s.Present && s.Valid && s.Ready
}

// NotUsable reports an index of the registry's name that is present but
// not valid or not ready (MTIX-95.44).
func (s RegistryIndexState) NotUsable() bool {
	return s.Present && !s.Usable()
}

// QualifiedName is the index's name, schema-qualified when the schema is
// known, for messages that name it.
func (s RegistryIndexState) QualifiedName() string {
	if s.SchemaIdent == "" {
		return RegistryIndexName
	}
	return s.SchemaIdent + "." + RegistryIndexName
}

// RegistryIndex reads the state of the node-number registry index on the
// sync_events table the search_path resolves (MTIX-95.44). It reads only
// the system catalog, so it needs no privilege on the sync tables.
func (p *Pool) RegistryIndex(ctx context.Context) (RegistryIndexState, error) {
	if p == nil || p.p == nil {
		return RegistryIndexState{}, fmt.Errorf("RegistryIndex: pool not open")
	}
	return readRegistryIndex(ctx, p.p)
}

// readRegistryIndex reads the registry index state through q
// (MTIX-95.44).
func readRegistryIndex(ctx context.Context, q queryRower) (RegistryIndexState, error) {
	s := RegistryIndexState{Present: true}
	// The index of the registry's name on sync_events, found by the table
	// it indexes (indrelid), with its flags and its schema, raw and
	// quoted server-side. No row: the hub has no such index.
	err := q.QueryRow(ctx, `
		SELECT i.indisvalid, i.indisready, n.nspname::text, pg_catalog.quote_ident(n.nspname)
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE i.indrelid = pg_catalog.to_regclass('sync_events') AND c.relname = $1`,
		RegistryIndexName,
	).Scan(&s.Valid, &s.Ready, &s.Schema, &s.SchemaIdent)
	if errors.Is(err, pgx.ErrNoRows) {
		return RegistryIndexState{}, nil
	}
	if err != nil {
		return RegistryIndexState{}, fmt.Errorf("read the registry index state: %s", redact.DSN(err.Error()))
	}
	return s, nil
}

// IsRegistryIndexConflict reports whether err is PostgreSQL's refusal to
// build the registry index over duplicate creates: a unique violation
// that names the index (MTIX-95.44).
func IsRegistryIndexConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == RegistryIndexName
}

// buildRegistryIndex is the part of EnsureRegistryIndex that runs under
// the session advisory lock, on conn, with the version gate open
// (MTIX-95.44). A valid and ready index is left as it is. Every refusal
// check runs first, so a refusal changes nothing: the duplicate creates to
// leave out are read and checked, and only then is an index of the
// registry's name that is not valid or not ready dropped and the index
// built CONCURRENTLY, leaving them out by name.
func buildRegistryIndex(ctx context.Context, conn *pgxpool.Conn, res IndexResult) (IndexResult, error) {
	state, err := readRegistryIndex(ctx, conn)
	if err != nil {
		return res, fmt.Errorf("EnsureRegistryIndex: %w", err)
	}
	res.State = state
	if state.Usable() {
		return res, nil // idempotent: already present, valid and ready
	}
	leftOut, err := registryLeftOut(ctx, conn)
	if err != nil {
		return res, err
	}
	if state.Present {
		if dropErr := dropRegistryIndex(ctx, conn, state); dropErr != nil {
			return res, dropErr
		}
		res.Rebuilt, res.State = true, RegistryIndexState{}
	}
	if buildErr := createRegistryIndex(ctx, conn, leftOut); buildErr != nil {
		return res, buildErr
	}
	if res.State, err = readRegistryIndex(ctx, conn); err != nil {
		return res, fmt.Errorf("EnsureRegistryIndex: %w", err)
	}
	if !res.State.Usable() {
		return res, fmt.Errorf("EnsureRegistryIndex: the registry index was built but is not valid and ready")
	}
	res.Added, res.LeftOut = true, len(leftOut)
	return res, nil
}

// dropRegistryIndex drops the registry index CONCURRENTLY, by its schema
// and name quoted server-side (SQL Rule 1a), outside any transaction
// (MTIX-95.44).
func dropRegistryIndex(ctx context.Context, conn *pgxpool.Conn, state RegistryIndexState) error {
	var stmt string
	if err := conn.QueryRow(ctx, `SELECT pg_catalog.format('DROP INDEX CONCURRENTLY IF EXISTS %I.%I', $1::text, $2::text)`,
		state.Schema, RegistryIndexName).Scan(&stmt); err != nil {
		return fmt.Errorf("EnsureRegistryIndex: prepare the drop: %s", redact.DSN(err.Error()))
	}
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("EnsureRegistryIndex: drop the registry index %s, which is not valid or not ready "+
			"(run mtix sync migrate --yes as the table owner): %s", state.QualifiedName(), redact.DSN(err.Error()))
	}
	return nil
}

// registryLeftOut returns, sorted, the event id of every create the
// registry index leaves out: every create that is not the first create
// (lowest event id, the sweep's winner) of its (project_prefix, node_id),
// in every project (MTIX-95.44). It refuses, before anything is dropped or
// built, when a left-out create belongs neither to the winner's node (same
// effective uid, ADR-003 §2) nor to its node's remap row for that number,
// or when the list is above MaxRegistryLeftOut or MaxRegistryLeftOutBytes.
// The slice is never nil, so an empty list builds migration 009's index.
func registryLeftOut(ctx context.Context, conn *pgxpool.Conn) ([]string, error) {
	ids, losers, err := scanLeftOut(ctx, conn)
	if err != nil {
		return nil, err
	}
	notYet, elsewhere, err := unrecordedLeftOut(ctx, conn, losers)
	if err != nil {
		return nil, err
	}
	if len(elsewhere) > 0 {
		return nil, fmt.Errorf("registry index not built: %d duplicate create(s) belong to a node already "+
			"recorded at another number, and the remap ledger records one number per node: %s; pushes keep "+
			"working; a durable design for such hubs is tracked as MTIX-97.12",
			len(elsewhere), strings.Join(elsewhere, ", "))
	}
	if notYet > 0 {
		return nil, fmt.Errorf("registry index not built: %d duplicate create(s) on the hub are not recorded yet; "+
			"run mtix sync migrate --yes again, which records them first", notYet)
	}
	if len(ids) > MaxRegistryLeftOut || idBytes(ids) > MaxRegistryLeftOutBytes {
		return nil, capRefusal(ids)
	}
	return ids, nil
}

// idBytes is the total length of ids.
func idBytes(ids []string) int {
	n := 0
	for _, id := range ids {
		n += len(id)
	}
	return n
}

// capRefusal is the exact refusal of a left-out list the registry index
// cannot name: above the cap, above the byte limit, or refused by
// PostgreSQL as too large (SQLSTATE 54000) (MTIX-95.44).
func capRefusal(ids []string) error {
	return fmt.Errorf("registry index not built: the hub holds %d duplicate creates (%d bytes of event ids), "+
		"more than the registry index can leave out (%d creates, %d bytes); pushes keep working, and a push "+
		"whose create takes a number already in use is still renumbered; a durable design for such hubs is "+
		"tracked as MTIX-97.12", len(ids), idBytes(ids), MaxRegistryLeftOut, MaxRegistryLeftOutBytes)
}

// leftOutLoser is a left-out create of a node that is not the winner's:
// its event id, its node's effective uid, and its number.
type leftOutLoser struct {
	eventID, uid, project, path string
}

// scanLeftOut reads the creates the index leaves out: their event ids,
// sorted, and each one whose node is not the winner's (MTIX-95.44).
func scanLeftOut(ctx context.Context, conn *pgxpool.Conn) ([]string, []leftOutLoser, error) {
	// Every create ranked within its (project, number) by event id; rank
	// 1 is the winner the index keeps. The effective uid is the stored uid,
	// or the create's own event id when it has none (ADR-003 §2). Reads
	// sync_events only.
	rows, err := conn.Query(ctx, `
		WITH creates AS (
		    SELECT event_id, project_prefix, node_id,
		        CASE WHEN uid IS NULL OR uid = '' THEN event_id ELSE uid END AS eff_uid,
		        row_number() OVER w AS rnk,
		        first_value(CASE WHEN uid IS NULL OR uid = '' THEN event_id ELSE uid END) OVER w AS winner_uid
		    FROM sync_events
		    WHERE op_type = 'create_node'
		    WINDOW w AS (PARTITION BY project_prefix, node_id ORDER BY event_id)
		)
		SELECT event_id, CASE WHEN eff_uid = winner_uid THEN '' ELSE eff_uid END, project_prefix, node_id
		FROM creates WHERE rnk > 1
		ORDER BY event_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("EnsureRegistryIndex: scan duplicate creates: %s", redact.DSN(err.Error()))
	}
	defer rows.Close()
	ids := []string{}
	var losers []leftOutLoser
	for rows.Next() {
		var l leftOutLoser
		if err := rows.Scan(&l.eventID, &l.uid, &l.project, &l.path); err != nil {
			return nil, nil, fmt.Errorf("EnsureRegistryIndex: scan duplicate create: %w", err)
		}
		ids = append(ids, l.eventID)
		if l.uid != "" {
			losers = append(losers, l)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("EnsureRegistryIndex: duplicate creates: %w", err)
	}
	return ids, losers, nil
}

// unrecordedLeftOut checks each left-out create of a loser node against
// the remap ledger (MTIX-95.44): it counts those whose node has no remap
// row yet, and returns, sorted, the event ids of those whose node's remap
// row is for another number, which the ledger, one row per node, cannot
// record. It reads the ledger only when there is a loser to look up.
func unrecordedLeftOut(ctx context.Context, conn *pgxpool.Conn, losers []leftOutLoser) (int, []string, error) {
	if len(losers) == 0 {
		return 0, nil, nil
	}
	var event, uid, project, path []string
	for _, l := range losers {
		event, uid, project, path = append(event, l.eventID), append(uid, l.uid), append(project, l.project),
			append(path, l.path)
	}
	var notYet int
	var elsewhere []string
	// For each left-out loser create: no remap row for its node (not yet
	// recorded), or a remap row for another project or number (recorded
	// elsewhere).
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE r.uid IS NULL),
		       COALESCE(array_agg(l.event_id ORDER BY l.event_id) FILTER (WHERE r.uid IS NOT NULL
		           AND (r.project_prefix <> l.project OR r.old_display_path <> l.path)), '{}')
		FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) AS l(event_id, uid, project, path)
		LEFT JOIN node_renumber_remaps r ON r.uid = l.uid`,
		event, uid, project, path).Scan(&notYet, &elsewhere); err != nil {
		return 0, nil, fmt.Errorf("EnsureRegistryIndex: check the remap ledger: %s", redact.DSN(err.Error()))
	}
	return notYet, elsewhere, nil
}

// createRegistryIndex builds the registry index CONCURRENTLY, outside any
// transaction, leaving out the creates in leftOut by name (MTIX-95.44).
// The statement is built on the server by format(): %I quotes the index
// name and %L the operation and the list of event ids (SQL Rule 1a); an
// empty list gives exactly migration 009's definition. The build may run
// longer than the pool's statement timeout, so the timeout is lifted for
// it and put back after. A build that fails is dropped again, so no index
// that is not valid is left behind.
func createRegistryIndex(ctx context.Context, conn *pgxpool.Conn, leftOut []string) (err error) {
	var stmt string
	if prepErr := conn.QueryRow(ctx, `
		SELECT pg_catalog.format(
		    'CREATE UNIQUE INDEX CONCURRENTLY %I ON sync_events (project_prefix, node_id) WHERE op_type = %L%s',
		    $1::text, 'create_node'::text,
		    CASE WHEN COALESCE(pg_catalog.cardinality($2::text[]), 0) = 0 THEN ''
		         ELSE pg_catalog.format(' AND event_id <> ALL (%L::text[])', $2::text[]) END)`,
		RegistryIndexName, leftOut).Scan(&stmt); prepErr != nil {
		return fmt.Errorf("EnsureRegistryIndex: prepare the build: %s", redact.DSN(prepErr.Error()))
	}
	restore, err := liftStatementTimeout(ctx, conn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restore()) }()
	if _, buildErr := conn.Exec(ctx, stmt); buildErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(buildErr, &pgErr) && pgErr.Code == "54000" {
			return errors.Join(capRefusal(leftOut), dropFailedBuild(ctx, conn))
		}
		return errors.Join(fmt.Errorf("EnsureRegistryIndex: build the registry index (run mtix sync migrate --yes "+
			"again as the table owner): %s", redact.DSN(buildErr.Error())), dropFailedBuild(ctx, conn))
	}
	return nil
}

// dropFailedBuild drops what a failed build left behind, an index that is
// not valid or not ready, on the same connection, even when ctx has ended
// (MTIX-95.44). It returns an error naming the fix when the drop fails.
func dropFailedBuild(ctx context.Context, conn *pgxpool.Conn) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), registryCleanupBudget)
	defer cancel()
	state, err := readRegistryIndex(cctx, conn)
	if err != nil {
		return fmt.Errorf("the failed build may have left the registry index not valid; "+
			"mtix sync doctor reports it: %w", err)
	}
	if !state.NotUsable() {
		return nil
	}
	return dropRegistryIndex(cctx, conn, state)
}

// liftStatementTimeout sets statement_timeout to 0 on conn for the index
// build, which may outlast the pool's timeout (MTIX-48), and returns the
// function that sets it back; a connection whose setting cannot be set
// back is closed, so the pool never hands it out again (MTIX-95.44).
// Session settings go through set_config with bound values (SQL Rule 1a).
func liftStatementTimeout(ctx context.Context, conn *pgxpool.Conn) (func() error, error) {
	var prev string
	if err := conn.QueryRow(ctx, `SELECT pg_catalog.current_setting('statement_timeout')`).Scan(&prev); err != nil {
		return nil, fmt.Errorf("EnsureRegistryIndex: read statement_timeout: %s", redact.DSN(err.Error()))
	}
	if _, err := conn.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '0', false)`); err != nil {
		return nil, fmt.Errorf("EnsureRegistryIndex: lift statement_timeout: %s", redact.DSN(err.Error()))
	}
	return func() error {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), registryCleanupBudget)
		defer cancel()
		_, err := conn.Exec(rctx, `SELECT pg_catalog.set_config('statement_timeout', $1, false)`, prev)
		if err == nil {
			return nil
		}
		return errors.Join(fmt.Errorf("EnsureRegistryIndex: restore statement_timeout: %s", redact.DSN(err.Error())),
			conn.Conn().Close(rctx))
	}, nil
}
