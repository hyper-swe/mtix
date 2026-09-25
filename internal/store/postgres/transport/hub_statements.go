// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// Statement templates of `mtix sync harden` (MTIX-95.1). PostgreSQL cannot
// bind identifiers, so each statement is built on the server by format()
// with bound arguments (SQL Rule 1a): %I quotes a role, schema or object
// name, and %s receives only a keyword from statementKeywords or, in a
// function template, the function's argument types, which argTypesPattern
// admits (MTIX-95.1.7). Go never concatenates a name into SQL. PUBLIC is
// literal text. A function template binds its argument types last.
const (
	sqlFmtRevokeRelation            = `SELECT format('REVOKE ALL ON %s %I.%I FROM %I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeRelationPublic      = `SELECT format('REVOKE ALL ON %s %I.%I FROM PUBLIC CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeRelationGrantOption = `SELECT format('REVOKE GRANT OPTION FOR ALL ON %s %I.%I FROM %I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeFunction            = `SELECT format('REVOKE ALL ON FUNCTION %1$I.%2$I(%4$s) FROM %3$I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeFunctionPublic      = `SELECT format('REVOKE ALL ON FUNCTION %1$I.%2$I(%3$s) FROM PUBLIC CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeFunctionGrantOption = `SELECT format('REVOKE GRANT OPTION FOR ALL ON FUNCTION %1$I.%2$I(%4$s) FROM %3$I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeDefaultSchema       = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON %s FROM %I CASCADE', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtRevokeDefaultSchemaPublic = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I IN SCHEMA %I REVOKE ALL ON %s FROM PUBLIC CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeDefaultGlobal       = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE ALL ON %s FROM %I CASCADE', $1::text, $2::text, $3::text)`
	sqlFmtRevokeDefaultGlobalPublic = `SELECT format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE ALL ON %s FROM PUBLIC CASCADE', $1::text, $2::text)`
	sqlFmtRevokeMembership          = `SELECT format('REVOKE %I FROM %I GRANTED BY %I', $1::text, $2::text, $3::text)`
	sqlFmtManualRevokeMembership    = `SELECT format('REVOKE %I FROM %I', $1::text, $2::text)`
	sqlFmtEnableTrigger             = `SELECT format('ALTER TABLE %I.%I ENABLE TRIGGER %I', $1::text, $2::text, $3::text)`
	sqlFmtGrantRelation             = `SELECT format('GRANT %s ON %s %I.%I TO %I', $1::text, $2::text, $3::text, $4::text, $5::text)`
	sqlFmtGrantColumn               = `SELECT format('GRANT %s (%I) ON TABLE %I.%I TO %I', $1::text, $2::text, $3::text, $4::text, $5::text)`
	sqlFmtGrantFunction             = `SELECT format('GRANT EXECUTE ON FUNCTION %1$I.%2$I(%4$s) TO %3$I', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtManualOwnerFunction       = `SELECT format('ALTER FUNCTION %1$I.%2$I(%4$s) OWNER TO %3$I', $1::text, $2::text, $3::text, $4::text)`
	sqlFmtManualOwnerSequence       = `SELECT format('ALTER SEQUENCE %I.%I OWNER TO %I', $1::text, $2::text, $3::text)`
)

// statementKeywords are the only values a template's %s may receive: object
// classes and the privilege names a re-grant may carry.
var statementKeywords = map[string]bool{
	"TABLE": true, "SEQUENCE": true, "TABLES": true, "SEQUENCES": true, "FUNCTIONS": true,
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "TRUNCATE": true,
	"REFERENCES": true, "TRIGGER": true, "USAGE": true, "MAINTAIN": true,
}

// argTypesPattern is the only form of a function's argument types a
// template's %s may receive: lower-case type names separated by ", ", as
// PostgreSQL lists the mtix functions' identity signatures, or nothing
// (MTIX-95.1.7). A type written with a space, a quote or a parenthesis is
// refused, so the statement is reported, never run.
var argTypesPattern = regexp.MustCompile(`^(?:[a-z][a-z0-9_]*(?:, [a-z][a-z0-9_]*)*)?$`)

// Action ranks: the order --apply runs statements in. The owner first grants
// again what a kept role holds by another role's grant; then grant options
// go, so their CASCADE removes every regrant made through them.
const (
	rankRegrant = iota
	rankGrantOption
	rankRoleGrant
	rankPublicGrant
	rankDefaultACL
	rankMembership
	rankGuard
)

// action is one change `mtix sync harden --apply` makes: a format()
// template and its bound parameters, or an embedded migration to run. A
// function statement also binds the function's argument types, last.
type action struct {
	name      string // short label for tests and ordering
	query     string
	params    []string
	function  bool   // argTypes is bound after params (MTIX-95.1.7)
	argTypes  string // the function's argument types, when function is set
	migration string
	rank      int
}

// shape is a stable one-line description of a, for tests and sorting.
func (a *action) shape() string {
	if a.migration != "" {
		return "migration " + a.migration
	}
	s := strings.TrimSpace(a.name + " " + strings.Join(a.params, " "))
	if a.function && a.argTypes != "" {
		s += " (" + a.argTypes + ")"
	}
	return s
}

// args returns a's parameters as query arguments, after checking that each
// is an allowed keyword or a valid identifier, and that a function's
// argument types have the form argTypesPattern admits (SQL Rule 1a).
func (a *action) args() ([]any, error) {
	out := make([]any, 0, len(a.params)+1)
	for _, p := range a.params {
		if !statementKeywords[p] && !model.ValidHubRoleName(p) {
			return nil, fmt.Errorf("statement parameter %q is not an identifier mtix names: %w",
				p, model.ErrInvalidInput)
		}
		out = append(out, p)
	}
	if a.function {
		if !argTypesPattern.MatchString(a.argTypes) {
			return nil, fmt.Errorf("function argument types %q are not a form mtix names: %w",
				a.argTypes, model.ErrInvalidInput)
		}
		out = append(out, a.argTypes)
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

// newFunctionAction is newAction for a statement on a function, which
// names it by its argument types too, bound after params (MTIX-95.1.7).
func newFunctionAction(name, query string, rank int, argTypes string, params ...string) *action {
	a := &action{name: name, query: query, params: params, function: true, argTypes: argTypes, rank: rank}
	if _, err := a.args(); err != nil {
		return nil
	}
	return a
}

// guardMigrationAction restores TRUNCATE guards by running the guard
// migration, which creates each guard that is absent and replaces one
// whose trigger calls another function; a guard already in place is left
// as it is (MTIX-95.1, MTIX-95.7).
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
		return "run migration " + a.migration + " (creates each missing guard and replaces one that executes another function)", nil
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

// planActions returns the fixes --apply runs, in order, one per distinct
// statement. It plans nothing unless a finding has a fix; when one does,
// the fixes of the information items run too.
func planActions(findings, info []finding) []*action {
	fixable := false
	for i := range findings {
		fixable = fixable || findings[i].fix != nil
	}
	if !fixable {
		return nil
	}
	all := append(append([]finding{}, findings...), info...)
	seen := map[string]bool{}
	var out []*action
	for i := range all {
		a := all[i].fix
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
