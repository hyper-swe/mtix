// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
)

// Finding kinds: what a PrivilegeFinding is about (MTIX-95.1).
const (
	FindingKindTable      = "table"
	FindingKindSequence   = "sequence"
	FindingKindFunction   = "function"
	FindingKindDefaultACL = "default_acl"
	FindingKindTrigger    = "trigger"
	FindingKindRole       = "role"
)

// Finding sources: how a role holds the access a finding reports
// (MTIX-95.1). A trigger finding uses missing or disabled; object_owner is
// a role other than the sync tables' owner that owns an mtix function or
// sequence; grantor is a kept role's privilege granted by a role other
// than the owner, which --apply grants again from the owner; createrole is
// a CREATEROLE role before PostgreSQL 16; superuser_membership is a role
// that can SET ROLE to a superuser, or that holds ADMIN OPTION on a role
// that can (MTIX-95.1.4).
const (
	FindingViaGrant           = "grant"
	FindingViaGrantOption     = "grant_option"
	FindingViaMembership      = "membership"
	FindingViaOwnerMembership = "owner_membership"
	FindingViaDefaultACL      = "default_acl"
	FindingViaMissing         = "missing"
	FindingViaDisabled        = "disabled"
	FindingViaObjectOwner     = "object_owner"
	FindingViaGrantor         = "grantor"
	FindingViaCreateRole      = "createrole"
	FindingViaSuperuser       = "superuser_membership"
)

// ScopeClusterWide marks a change that applies to every database on the
// server: a role membership (MTIX-95.1).
const ScopeClusterWide = "cluster-wide"

// Caller scopes: whether verification checked the connecting role
// (MTIX-95.1). It is checked unless it owns the sync tables, is a superuser
// or is kept.
const (
	CallerOwner     = "owner"
	CallerSuperuser = "superuser"
	CallerKept      = "kept"
	CallerChecked   = "checked"
)

var (
	// ErrHardenNotOwner refuses hub hardening by a role that does not own
	// every sync table (MTIX-95.1). The message is fixed and names nothing.
	ErrHardenNotOwner = errors.New("refused: the connecting role does not own every sync table; " +
		"run mtix sync harden as the role that owns them")

	// ErrSyncSchemaIncomplete means a sync table is missing, or the sync
	// tables are not in one schema (MTIX-95.1).
	ErrSyncSchemaIncomplete = errors.New("the hub schema is incomplete; run mtix sync init first")

	// ErrHubWarning means a statement raised a PostgreSQL WARNING, which
	// harden treats as a failure; the transaction was rolled back
	// (MTIX-95.1).
	ErrHubWarning = errors.New("a server WARNING is a failure; the transaction was rolled back and nothing was changed")
)

// PrivilegeFinding is one way a role other than the owners and the kept
// roles can use the sync tables, sequences or mtix functions, or one TRUNCATE
// guard that is missing or disabled (MTIX-95.1). Fix is the statement
// `mtix sync harden --apply` runs for it; a finding without Fix is
// remaining exposure, and Manual, when set, is the statement an
// administrator runs.
type PrivilegeFinding struct {
	Role       string   `json:"role,omitempty"`
	Object     string   `json:"object"`
	Kind       string   `json:"kind"`
	Privileges []string `json:"privileges,omitempty"`
	Via        string   `json:"via"`
	Scope      string   `json:"scope,omitempty"`
	Fix        string   `json:"fix,omitempty"`
	Manual     string   `json:"manual,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// PrivilegeReport is the result of verifying the hub's privileges
// (MTIX-95.1). Findings fail verification; Info items do not (another
// creator's default privileges). Statements lists, in order, what
// `mtix sync harden --apply` would run. CallerScope says whether the
// connecting role, Caller, was checked.
type PrivilegeReport struct {
	Schema      string             `json:"schema"`
	Owners      []string           `json:"owners"`
	Caller      string             `json:"caller"`
	CallerScope string             `json:"caller_scope"`
	KeptRoles   []string           `json:"kept_roles"`
	Findings    []PrivilegeFinding `json:"findings"`
	Info        []PrivilegeFinding `json:"info,omitempty"`
	Statements  []string           `json:"statements,omitempty"`
}

// Clean reports whether verification passed: no findings.
func (r *PrivilegeReport) Clean() bool {
	return r != nil && len(r.Findings) == 0
}

// Pending returns how many findings --apply can fix.
func (r *PrivilegeReport) Pending() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, f := range r.Findings {
		if f.Fix != "" {
			n++
		}
	}
	return n
}

// HardenRequest configures Pool.Harden (MTIX-95.1). Warnings must be the
// log whose Record the pool was opened with (Options.OnNotice) when Apply
// is set: a REVOKE that PostgreSQL could not carry out raises only a
// WARNING, and Harden fails on one.
type HardenRequest struct {
	Apply     bool
	KeepRoles []string
	Warnings  *WarningLog
}

// HardenResult is what Pool.Harden found and did (MTIX-95.1). After is the
// verification after --apply, or Before again when there was nothing to
// change.
type HardenResult struct {
	Applied  bool             `json:"applied"`
	Before   *PrivilegeReport `json:"before"`
	Executed []string         `json:"executed,omitempty"`
	After    *PrivilegeReport `json:"after,omitempty"`
}

// VerifyHubPrivileges verifies the hub's privileges in a READ ONLY
// transaction, which cannot change anything, for any calling role
// (MTIX-95.1). Only the table owners, superusers and keepRoles may use the
// sync objects; the calling role is checked too unless it is one of them.
// Every TRUNCATE guard must exist and be enabled.
func (p *Pool) VerifyHubPrivileges(ctx context.Context, keepRoles []string) (*PrivilegeReport, error) {
	if p == nil || p.p == nil {
		return nil, fmt.Errorf("verify hub privileges: pool not open")
	}
	kept, err := model.ValidateKeepRoles(keepRoles)
	if err != nil {
		return nil, fmt.Errorf("verify hub privileges: %w", err)
	}
	report, err := p.verify(ctx, kept, false)
	if err != nil {
		return nil, fmt.Errorf("verify hub privileges: %w", err)
	}
	return report, nil
}

// Harden runs `mtix sync harden` (MTIX-95.1). It refuses with
// ErrHardenNotOwner unless the caller owns every sync table, then verifies
// in a READ ONLY transaction. Without Apply, or when verification finds
// nothing it can fix, it changes nothing and issues no DDL. With Apply it
// revokes, in one transaction under the migration advisory lock, every
// privilege of PUBLIC and of each role other than the owners and the kept
// roles on the sync tables, sequences and mtix functions, the owners'
// matching default privileges and read-all memberships it may revoke,
// and restores the TRUNCATE guards. A statement that raises a WARNING fails
// the run with ErrHubWarning and rolls everything back. It then verifies
// again.
func (p *Pool) Harden(ctx context.Context, req HardenRequest) (*HardenResult, error) {
	if p == nil || p.p == nil {
		return nil, fmt.Errorf("harden: pool not open")
	}
	kept, err := model.ValidateKeepRoles(req.KeepRoles)
	if err != nil {
		return nil, fmt.Errorf("harden: %w", err)
	}
	if req.Apply && req.Warnings == nil {
		return nil, fmt.Errorf("harden: applying needs the pool's warning log: %w", model.ErrInvalidInput)
	}
	before, err := p.verify(ctx, kept, true)
	if err != nil {
		return nil, fmt.Errorf("harden: %w", err)
	}
	result := &HardenResult{Before: before}
	if !req.Apply {
		return result, nil
	}
	if before.Pending() == 0 {
		result.After = before
		return result, nil
	}
	if result.Executed, err = p.applyHardening(ctx, kept, req.Warnings); err != nil {
		return nil, fmt.Errorf("harden: %w", err)
	}
	result.Applied = true
	if result.After, err = p.verify(ctx, kept, true); err != nil {
		return nil, fmt.Errorf("harden: verify after apply: %w", err)
	}
	return result, nil
}

// verify builds the privilege report in a READ ONLY transaction, which
// cannot issue DDL; it checks that the server runs it read-only.
func (p *Pool) verify(ctx context.Context, kept []string, requireOwner bool) (*PrivilegeReport, error) {
	tx, err := p.p.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin read-only: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // read-only: nothing to keep
	var readOnly string
	// The server's view of this transaction's access mode.
	if err = tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only')`).Scan(&readOnly); err != nil {
		return nil, fmt.Errorf("check read-only: %w", err)
	}
	if readOnly != "on" {
		return nil, fmt.Errorf("verification transaction is not read-only")
	}
	report, _, err := buildReport(ctx, tx, kept, requireOwner)
	return report, err
}

// applyHardening runs every fix in one transaction under the migration
// advisory lock, with a 5-second lock timeout so a busy hub fails the run
// fast instead of queueing pushes and pulls behind it (F-44). The catalog
// is read again under the lock. Any WARNING fails the run.
func (p *Pool) applyHardening(ctx context.Context, kept []string, warnings *WarningLog) ([]string, error) {
	tx, err := p.p.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	if _, err = tx.Exec(ctx, `SELECT set_config('lock_timeout', '5s', true)`); err != nil {
		return nil, fmt.Errorf("set lock timeout: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, AdvisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire advisory lock: %w", err)
	}
	_, actions, err := buildReport(ctx, tx, kept, true)
	if err != nil {
		return nil, err
	}
	// The guard migration creates objects in the search_path's first
	// schema: when it would run, refuse unless that is the sync tables'
	// schema. Privilege fixes create nothing and always proceed
	// (MTIX-95.7).
	if runsGuardMigration(actions) {
		if err := checkSchemaFirst(ctx, tx); err != nil {
			return nil, err
		}
	}
	if w := warnings.Take(); len(w) > 0 {
		return nil, fmt.Errorf("reading the hub raised a server WARNING: %s: %w", strings.Join(w, "; "), ErrHubWarning)
	}
	executed := make([]string, 0, len(actions))
	for _, a := range actions {
		stmt, err := execAction(ctx, tx, a)
		if err != nil {
			return nil, err
		}
		if w := warnings.Take(); len(w) > 0 {
			return nil, fmt.Errorf("%s raised a server WARNING: %s: %w", stmt, strings.Join(w, "; "), ErrHubWarning)
		}
		executed = append(executed, stmt)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return executed, nil
}

// runsGuardMigration reports whether actions include the guard migration
// (MTIX-95.7).
func runsGuardMigration(actions []*action) bool {
	for _, a := range actions {
		if a.migration != "" {
			return true
		}
	}
	return false
}

// execAction runs one fix: its format()-built statement, or the embedded
// guard migration (constant DDL). It returns the statement's text.
func execAction(ctx context.Context, tx pgx.Tx, a *action) (string, error) {
	stmt, err := a.render(ctx, tx)
	if err != nil {
		return "", err
	}
	sql := stmt
	if a.migration != "" {
		if sql, err = migrations.Read(a.migration); err != nil {
			return "", fmt.Errorf("guard migration: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return "", fmt.Errorf("%s: %w", stmt, err)
	}
	return stmt, nil
}

// buildReport loads the catalog in tx, computes the findings and renders
// each fix, manual statement and the ordered statement list.
func buildReport(ctx context.Context, tx pgx.Tx, kept []string, requireOwner bool) (*PrivilegeReport, []*action, error) {
	cat, err := loadCatalog(ctx, tx, kept, requireOwner)
	if err != nil {
		return nil, nil, err
	}
	keptSet := map[string]bool{}
	for _, k := range kept {
		keptSet[k] = true
	}
	findings, info := computeFindings(cat, keptSet)
	report := &PrivilegeReport{Schema: cat.schema, Owners: cat.ownerNames(),
		Caller: cat.roleName(cat.current), CallerScope: cat.callerScope(keptSet),
		KeptRoles: append([]string{}, kept...), Findings: []PrivilegeFinding{}}
	if report.Findings, err = renderFindings(ctx, tx, findings); err != nil {
		return nil, nil, err
	}
	if len(info) > 0 {
		if report.Info, err = renderFindings(ctx, tx, info); err != nil {
			return nil, nil, err
		}
	}
	actions := planActions(findings, info)
	for _, a := range actions {
		stmt, err := a.render(ctx, tx)
		if err != nil {
			return nil, nil, err
		}
		report.Statements = append(report.Statements, stmt)
	}
	return report, actions, nil
}

// renderFindings fills each finding's Fix and Manual statements.
func renderFindings(ctx context.Context, q queryRower, fs []finding) ([]PrivilegeFinding, error) {
	out := make([]PrivilegeFinding, 0, len(fs))
	for _, f := range fs {
		pf := f.PrivilegeFinding
		var err error
		if f.fix != nil {
			if pf.Fix, err = f.fix.render(ctx, q); err != nil {
				return nil, err
			}
		}
		if f.manual != nil {
			if pf.Manual, err = f.manual.render(ctx, q); err != nil {
				return nil, err
			}
		}
		out = append(out, pf)
	}
	return out, nil
}
