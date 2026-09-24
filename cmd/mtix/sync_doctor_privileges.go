// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// hubPrivilegesName is the doctor's name for the privilege check.
const hubPrivilegesName = "hub-privileges"

// hubPrivilegesFix is the command that addresses the check: the dry run,
// whose report says what --apply would change.
const hubPrivilegesFix = "mtix sync harden"

// checkHubPrivileges runs the verification `mtix sync harden` runs, as any
// connecting role, and turns it into the doctor's hub-privileges check
// (MTIX-95.1). With sync.keep_roles unset, access by other roles, a missing
// or disabled guard, or a verification that could not run is a WARN: the
// check passes, so the doctor still exits 0 and nothing that gates on it
// stops. With sync.keep_roles set (strict mode) the same cases fail it. A
// clean hub passes. The hub is contacted only here, while the doctor runs.
func checkHubPrivileges(ctx context.Context, dsn string, hubReady bool, opts transport.Options) DoctorCheck {
	configured := ""
	if app.configSvc != nil {
		v, err := app.configSvc.Get("sync.keep_roles")
		if err != nil {
			return DoctorCheck{Name: hubPrivilegesName, Detail: err.Error()}
		}
		configured = v
	}
	kept, err := model.ParseKeepRoles(configured)
	if err != nil {
		// A value is set, so strict mode was intended: fail.
		return DoctorCheck{Name: hubPrivilegesName, Detail: "sync.keep_roles in .mtix/config.yaml: " + err.Error()}
	}
	strict := len(kept) > 0
	if !hubReady {
		return unverified(strict, "skipped (hub unreachable or schema not current); "+
			"fix the checks above, then run mtix sync doctor again")
	}
	report, err := verifyHubPrivileges(ctx, dsn, opts, kept)
	if err != nil {
		return unverified(strict, "could not verify hub privileges: "+err.Error()+
			"; check the hub connection, run mtix sync init if the hub schema is incomplete, "+
			"then run mtix sync doctor again")
	}
	return hubPrivilegesResult(report, strict)
}

// unverified reports a hub-privileges check that could not run: a WARN by
// default, never red, and a failure in strict mode (MTIX-95.1).
func unverified(strict bool, detail string) DoctorCheck {
	return DoctorCheck{Name: hubPrivilegesName, Pass: !strict, Warn: !strict, Detail: detail}
}

// verifyHubPrivileges opens a pool and runs the verification.
func verifyHubPrivileges(ctx context.Context, dsn string, opts transport.Options, kept []string) (*transport.PrivilegeReport, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := transport.New(cctx, dsn, opts)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	return pool.VerifyHubPrivileges(cctx, kept)
}

// hubPrivilegesResult grades a verification report: PASS when clean, WARN
// by default, FAIL in strict mode.
func hubPrivilegesResult(report *transport.PrivilegeReport, strict bool) DoctorCheck {
	check := DoctorCheck{Name: hubPrivilegesName, Pass: true}
	if report.Clean() {
		check.Detail = "only the table owner, superusers and the kept roles can use the sync tables; " +
			"every TRUNCATE guard is in place"
		return check
	}
	check.Fix, check.Findings = hubPrivilegesFix, report.Findings
	roles, kept, guards := findingRoles(report)
	var parts []string
	if strict {
		check.Pass = false
		parts = append(parts, "strict mode (sync.keep_roles: "+strings.Join(report.KeptRoles, ", ")+")")
	} else {
		check.Warn = true
	}
	if len(roles) > 0 {
		parts = append(parts, "roles other than the table owner"+keptSuffix(strict)+
			" can read or write the sync tables, or can reach them: "+strings.Join(roles, ", "))
	}
	if len(kept) > 0 {
		parts = append(parts, "kept roles that can grant their access on, or hold it by another role's grant: "+
			strings.Join(kept, ", "))
	}
	if guards {
		parts = append(parts, "a TRUNCATE guard is missing or disabled")
	}
	if !strict {
		parts = append(parts, "this may be fine when the database is reachable only from a private network")
	}
	parts = append(parts, "to restrict access, run mtix sync harden (a dry run), then mtix sync harden --apply "+
		"--keep-role <role> for each role that should keep access; setting sync.keep_roles turns on strict mode")
	check.Detail = strings.Join(parts, "; ")
	return check
}

// keptSuffix names the kept roles as allowed in strict mode.
func keptSuffix(strict bool) string {
	if strict {
		return " and the kept roles"
	}
	return ""
}

// findingRoles returns the distinct roles the report's findings name,
// sorted: those that are not kept, and the kept roles (whose findings are
// about passing access on or holding it by another role's grant); and
// whether a TRUNCATE guard finding is among them.
func findingRoles(report *transport.PrivilegeReport) (roles, kept []string, guards bool) {
	isKept := map[string]bool{}
	for _, k := range report.KeptRoles {
		isKept[k] = true
	}
	seen := map[string]bool{}
	for _, f := range report.Findings {
		if f.Kind == transport.FindingKindTrigger {
			guards = true
		}
		if f.Role == "" || seen[f.Role] {
			continue
		}
		seen[f.Role] = true
		if isKept[f.Role] {
			kept = append(kept, safeText(f.Role))
		} else {
			roles = append(roles, safeText(f.Role))
		}
	}
	sort.Strings(roles)
	sort.Strings(kept)
	return roles, kept, guards
}
