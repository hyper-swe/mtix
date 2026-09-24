// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// Statement templates of `mtix sync harden` (MTIX-95.1). PostgreSQL cannot
// bind identifiers, so each statement is built on the server by format()
// with bound arguments (SQL Rule 1a): %I quotes a role, schema or object
// name, and %s receives only a keyword from statementKeywords. Go never
// concatenates a name into SQL. PUBLIC is literal text.
const (
	sqlFmtRevokeRelation            = `SELECT format('REVOKE ALL ON %s %I.%I FROM %I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeRelationPublic      = `SELECT format('REVOKE ALL ON %s %I.%I FROM PUBLIC CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeRelationGrantOption = `SELECT format('REVOKE GRANT OPTION FOR ALL ON %s %I.%I FROM %I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeFunction            = `SELECT format('REVOKE ALL ON FUNCTION %I.%I() FROM %I CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeFunctionPublic      = `SELECT format('REVOKE ALL ON FUNCTION %I.%I() FROM PUBLIC CASCADE', $1::text, $2::text)`
	sqlFmtRevokeFunctionGrantOption = `SELECT format('REVOKE GRANT OPTION FOR ALL ON FUNCTION %I.%I() FROM %I CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeDefaultSchema       = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON %s FROM %I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeDefaultSchemaPublic = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON %s FROM PUBLIC CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeDefaultGlobal       = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE ALL ON %s FROM %I CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeDefaultGlobalPublic = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE ALL ON %s FROM PUBLIC CASCADE', $1::text, $2::text)`
	sqlFmtRevokeMembership          = `SELECT format('REVOKE %I FROM %I GRANTED BY %I', $1::text, $2::text, $3::text)`
	sqlFmtManualRevokeMembership    = `SELECT format('REVOKE %I FROM %I', $1::text, $2::text)`
	sqlFmtEnableTrigger             = `SELECT format('ALTER TABLE %I.%I ENABLE TRIGGER %I', $1::text, $2::text, $3::text)`
)

// statementKeywords are the only values a template's %s may receive.
var statementKeywords = map[string]bool{
	"TABLE": true, "SEQUENCE": true, "TABLES": true, "SEQUENCES": true, "FUNCTIONS": true,
}

// Action ranks: the order --apply runs statements in. Grant options go
// first, so their CASCADE removes every regrant made through them.
const (
	rankGrantOption = iota
	rankRoleGrant
	rankPublicGrant
	rankDefaultACL
	rankMembership
	rankGuard
)

// action is one change `mtix sync harden --apply` makes: a format()
// template and its bound parameters, or an embedded migration to run.
type action struct {
	name      string // short label for tests and ordering
	query     string
	params    []string
	migration string
	rank      int
}

// shape is a stable one-line description of a, for tests and sorting.
func (a *action) shape() string {
	if a.migration != "" {
		return "migration " + a.migration
	}
	return strings.TrimSpace(a.name + " " + strings.Join(a.params, " "))
}

// args returns a's parameters as query arguments, after checking that each
// is an allowed keyword or a valid identifier (SQL Rule 1a).
func (a *action) args() ([]any, error) {
	out := make([]any, 0, len(a.params))
	for _, p := range a.params {
		if !statementKeywords[p] && !model.ValidHubRoleName(p) {
			return nil, fmt.Errorf("statement parameter %q is not an identifier mtix names: %w",
				p, model.ErrInvalidInput)
		}
		out = append(out, p)
	}
	return out, nil
}

// newAction builds an action whose parameters are all valid, or returns
// nil when one is not: such a change is reported, never run.
func newAction(name, query string, rank int, params ...string) *action {
	a := &action{name: name, query: query, params: params, rank: rank}
	if _, err := a.args(); err != nil {
		return nil
	}
	return a
}

// guardMigrationAction restores missing TRUNCATE guards by running the
// guard migration, which creates each guard only when it is absent.
func guardMigrationAction() *action {
	return &action{migration: migrations.TruncateGuardFile, rank: rankGuard}
}

// queryRower is the part of a pgx transaction statements are built with.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// render returns the statement a runs, built by format() on the server.
// A migration action renders as a description of the file it runs.
func (a *action) render(ctx context.Context, q queryRower) (string, error) {
	if a.migration != "" {
		return "run migration " + a.migration + " (creates each missing guard)", nil
	}
	args, err := a.args()
	if err != nil {
		return "", err
	}
	var stmt string
	if err := q.QueryRow(ctx, a.query, args...).Scan(&stmt); err != nil {
		return "", fmt.Errorf("build statement %s: %w", a.name, err)
	}
	return stmt, nil
}

// planActions returns the fixes of findings in the order --apply runs
// them, one per distinct statement.
func planActions(findings []finding) []*action {
	seen := map[string]bool{}
	var out []*action
	for i := range findings {
		a := findings[i].fix
		if a == nil || seen[a.shape()] {
			continue
		}
		seen[a.shape()] = true
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		return out[i].shape() < out[j].shape()
	})
	return out
}
