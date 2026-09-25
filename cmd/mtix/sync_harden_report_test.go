// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// sampleReport is a report with every kind of line the text output shows.
func sampleReport() *transport.PrivilegeReport {
	return &transport.PrivilegeReport{
		Schema: "public", Owners: []string{"owner"}, KeptRoles: []string{"team"},
		Findings: []transport.PrivilegeFinding{
			{Role: "PUBLIC", Object: "public.audit_log", Kind: transport.FindingKindTable,
				Privileges: []string{"SELECT"}, Via: transport.FindingViaGrant,
				Fix: "REVOKE ALL ON TABLE public.audit_log FROM PUBLIC CASCADE"},
			{Role: "team", Object: "public.sync_projects", Kind: transport.FindingKindTable,
				Privileges: []string{"SELECT"}, Via: transport.FindingViaGrantOption,
				Fix: "REVOKE GRANT OPTION FOR ALL ON TABLE public.sync_projects FROM team CASCADE"},
			{Role: "reader", Object: "pg_read_all_data", Kind: transport.FindingKindRole,
				Via: transport.FindingViaMembership, Scope: transport.ScopeClusterWide,
				Manual: "REVOKE pg_read_all_data FROM reader"},
			{Object: "audit_log_no_truncate on public.audit_log", Kind: transport.FindingKindTrigger,
				Via: transport.FindingViaMissing, Fix: "run migration 016_append_only_truncate_guard.sql (creates each missing guard and replaces one that executes another function)"},
			{Role: "anon", Object: "default privileges of owner in schema public on tables",
				Kind: transport.FindingKindDefaultACL, Privileges: []string{"SELECT"},
				Via: transport.FindingViaDefaultACL,
				Fix: "ALTER DEFAULT PRIVILEGES FOR ROLE owner IN SCHEMA public REVOKE ALL ON TABLES FROM anon CASCADE"},
		},
		Info: []transport.PrivilegeFinding{
			{Role: "anon", Object: "default privileges of postgres in all schemas on tables",
				Kind: transport.FindingKindDefaultACL, Privileges: []string{"SELECT"}, Via: transport.FindingViaDefaultACL,
				Note: "another role's default privileges do not apply to tables the owner creates"},
		},
		Statements: []string{
			"REVOKE GRANT OPTION FOR ALL ON TABLE public.sync_projects FROM team CASCADE",
			"REVOKE ALL ON TABLE public.audit_log FROM PUBLIC CASCADE",
		},
	}
}

// TestPrintHardenResult_DryRun_ListsRolesBeforeDetail: the dry run names
// the affected roles before any detail, separates what --apply changes from
// what it cannot, and ends with the statements and the --apply command
// (MTIX-95.1).
func TestPrintHardenResult_DryRun_ListsRolesBeforeDetail(t *testing.T) {
	saveAndResetApp(t)
	var out bytes.Buffer
	require.NoError(t, printHardenResult(&out, &transport.HardenResult{Before: sampleReport()}, false, ""))
	text := out.String()

	order := []string{
		"dry run; nothing was changed",
		"Kept roles: team.",
		"Roles that lose their access with --apply: PUBLIC, anon.",
		"Kept roles that keep their access but can no longer grant it: team.",
		"Roles whose access --apply cannot remove: reader.",
		"Changes --apply would make:",
		"Access --apply cannot remove (an administrator must act):",
		"an administrator runs: REVOKE pg_read_all_data FROM reader",
		"Information (does not fail verification):",
		"another role's default privileges do not apply to tables the owner creates",
		"Statements --apply would run, in order:",
		"Then run: mtix sync harden --apply --keep-role team",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(text, want)
		require.Greaterf(t, i, last, "%q must follow the previous line in:\n%s", want, text)
		last = i
	}
	require.Contains(t, text, "[membership, cluster-wide]")
	require.Contains(t, text, "anon                         default privileges of owner in schema public on tables (SELECT)")
	require.NotContains(t, text, "default_acl default privileges", "the kind is not repeated for default privileges")
}

// TestPrintHardenResult_Apply_ReportsVerificationAndHint: --apply prints
// what it ran, the verification, and the config command for kept roles.
func TestPrintHardenResult_Apply_ReportsVerificationAndHint(t *testing.T) {
	saveAndResetApp(t)
	clean := &transport.PrivilegeReport{Schema: "public", Owners: []string{"owner"}}
	tests := []struct {
		name   string
		result *transport.HardenResult
		want   []string
	}{
		{"applied and clean", &transport.HardenResult{Applied: true, Executed: []string{"REVOKE x"},
			Before: sampleReport(), After: clean},
			[]string{"applied 1 statement(s):", "  REVOKE x", "Kept roles: none.", "verification passed",
				"To keep the same roles in later runs, run: mtix config set sync.keep_roles team"}},
		{"nothing to change", &transport.HardenResult{Before: clean, After: clean},
			[]string{"nothing to change", "verification passed"}},
		{"access remains", &transport.HardenResult{Applied: true, Before: sampleReport(), After: sampleReport()},
			[]string{"verification failed: access remains.", "Findings that remain:",
				"an administrator runs: REVOKE pg_read_all_data FROM reader"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, printHardenResult(&out, tt.result, true, "mtix config set sync.keep_roles team"))
			for _, want := range tt.want {
				require.Contains(t, out.String(), want)
			}
		})
	}
}

// TestPrintHardenResult_JSON_CarriesReportAndHint: --json prints the result
// with the config hint.
func TestPrintHardenResult_JSON_CarriesReportAndHint(t *testing.T) {
	saveAndResetApp(t)
	app.jsonOutput = true
	var out bytes.Buffer
	require.NoError(t, printHardenResult(&out, &transport.HardenResult{Before: sampleReport()}, false,
		"mtix config set sync.keep_roles team"))
	var got struct {
		Applied bool                       `json:"applied"`
		Before  *transport.PrivilegeReport `json:"before"`
		Hint    string                     `json:"keep_roles_hint"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, sampleReport(), got.Before)
	require.Equal(t, "mtix config set sync.keep_roles team", got.Hint)
}

// TestHardenOutcome_FinalVerification_MapsExitCode: exit 0 only when the
// last verification is clean.
func TestHardenOutcome_FinalVerification_MapsExitCode(t *testing.T) {
	clean := &transport.PrivilegeReport{}
	dirty := sampleReport()
	require.NoError(t, hardenOutcome(&transport.HardenResult{Before: clean}))
	require.ErrorIs(t, hardenOutcome(&transport.HardenResult{Before: dirty}), errHardenPending)
	require.NoError(t, hardenOutcome(&transport.HardenResult{Before: dirty, After: clean}))
	require.ErrorIs(t, hardenOutcome(&transport.HardenResult{Before: clean, After: dirty}), errHardenPending)
}

// TestSafeText_CatalogNames_QuotesUnprintable: a name read from the
// catalog cannot write control characters to the terminal.
func TestSafeText_CatalogNames_QuotesUnprintable(t *testing.T) {
	require.Equal(t, "mtix_team", safeText("mtix_team"))
	require.Equal(t, `"evil\x1b[2J"`, safeText("evil\x1b[2J"))
	require.Equal(t, `"t\u00e9am"`, safeText("t\u00e9am"))
}

// TestHardenErr_KnownSentinels_StayInChain: after the DSN scrub, the
// refusal, WARNING, schema and invalid-input sentinels stay in the error
// chain, so callers can test them with errors.Is; other errors keep only
// their scrubbed text (MTIX-95.1).
func TestHardenErr_KnownSentinels_StayInChain(t *testing.T) {
	for _, sentinel := range []error{
		transport.ErrHardenNotOwner, transport.ErrHubWarning,
		transport.ErrSyncSchemaIncomplete, model.ErrInvalidInput,
	} {
		err := hardenErr("", fmt.Errorf("harden: %w", sentinel))
		require.ErrorIs(t, err, sentinel)
		require.Equal(t, "mtix sync harden: "+sentinel.Error(), err.Error(), "the text is not repeated")
	}
	other := errors.New("boom")
	err := hardenErr("connect", fmt.Errorf("x: %w", other))
	require.NotErrorIs(t, err, other)
	require.Equal(t, "mtix sync harden connect: x: boom", err.Error())
}

// TestPrintHardenResult_Apply_ListsEveryRemainingFinding: after --apply,
// every finding that remains is listed, those --apply could fix included
// (for example a grant made while it ran), with the statement that fixes
// it (MTIX-95.1).
func TestPrintHardenResult_Apply_ListsEveryRemainingFinding(t *testing.T) {
	saveAndResetApp(t)
	var out bytes.Buffer
	result := &transport.HardenResult{Applied: true, Executed: []string{"REVOKE x"},
		Before: sampleReport(), After: sampleReport()}
	require.NoError(t, printHardenResult(&out, result, true, ""))
	text := out.String()
	require.Contains(t, text, "Findings that remain:")
	for _, want := range []string{
		"PUBLIC", "REVOKE ALL ON TABLE public.audit_log FROM PUBLIC CASCADE",
		"reader", "an administrator runs: REVOKE pg_read_all_data FROM reader",
	} {
		require.Contains(t, text, want)
	}
}

// TestPrintHardenScope_Caller_StatesWhetherChecked: the report says
// whether the connecting role was checked (MTIX-95.1).
func TestPrintHardenScope_Caller_StatesWhetherChecked(t *testing.T) {
	tests := []struct {
		name   string
		report transport.PrivilegeReport
		want   string
	}{
		{"owner", transport.PrivilegeReport{Caller: "owner", CallerScope: transport.CallerOwner},
			"Connecting role owner: not checked, it owns the sync tables."},
		{"superuser", transport.PrivilegeReport{Caller: "postgres", CallerScope: transport.CallerSuperuser},
			"Connecting role postgres: not checked, it is a superuser."},
		{"kept", transport.PrivilegeReport{Caller: "team", CallerScope: transport.CallerKept},
			"Connecting role team: not checked, it is a kept role."},
		{"checked", transport.PrivilegeReport{Caller: "admin", CallerScope: transport.CallerChecked},
			"Connecting role admin: checked."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printHardenScope(&buf, &tt.report)
			require.Contains(t, buf.String(), tt.want)
		})
	}
}

// TestPrintHardenResult_Apply_KeepRolesHintNamesStrictMode: the config
// command harden offers also turns on strict mode for mtix sync doctor, and
// the report says so and when strict mode fails: whenever mtix sync harden
// would report a finding, or when the check cannot run (MTIX-95.1,
// MTIX-95.1.4).
func TestPrintHardenResult_Apply_KeepRolesHintNamesStrictMode(t *testing.T) {
	saveAndResetApp(t)
	clean := &transport.PrivilegeReport{Schema: "public", Owners: []string{"owner"}}
	var out bytes.Buffer
	require.NoError(t, printHardenResult(&out, &transport.HardenResult{Applied: true, Before: clean, After: clean},
		true, "mtix config set sync.keep_roles team"))
	require.Contains(t, out.String(), "mtix config set sync.keep_roles team")
	require.Contains(t, out.String(), "also turns on strict mode for mtix sync doctor")
	text := strings.Join(strings.Fields(out.String()), " ")
	require.Contains(t, text, "its hub-privileges check then fails, instead of warning, whenever mtix sync harden "+
		"would report a finding, or when the check cannot run.")
	require.NotContains(t, text, "when any other role can use the sync tables", "the narrower description is gone")
}

// TestPrintHardenResult_OnlyCallerOwnerMembership_Hint: when the only
// finding left is the connecting role's own membership in the owner role,
// the report says how to verify clean (MTIX-95.1).
func TestPrintHardenResult_OnlyCallerOwnerMembership_Hint(t *testing.T) {
	saveAndResetApp(t)
	own := transport.PrivilegeFinding{Role: "admin", Object: "owner", Kind: transport.FindingKindRole,
		Via: transport.FindingViaOwnerMembership}
	other := transport.PrivilegeFinding{Role: "carol", Object: "owner", Kind: transport.FindingKindRole,
		Via: transport.FindingViaOwnerMembership}
	tests := []struct {
		name     string
		findings []transport.PrivilegeFinding
		want     bool
	}{
		{"only the caller's membership", []transport.PrivilegeFinding{own}, true},
		{"another member too", []transport.PrivilegeFinding{own, other}, false},
		{"another role's membership", []transport.PrivilegeFinding{other}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := &transport.PrivilegeReport{Schema: "public", Owners: []string{"owner"}, Caller: "admin",
				CallerScope: transport.CallerChecked, Findings: tt.findings}
			for _, apply := range []bool{false, true} {
				var out bytes.Buffer
				require.NoError(t, printHardenResult(&out,
					&transport.HardenResult{Applied: apply, Before: rep, After: rep}, apply, ""))
				hint := "The only finding is the connecting role's own membership in the owner role"
				if tt.want {
					require.Contains(t, out.String(), hint)
					require.Contains(t, out.String(), "--keep-role admin")
				} else {
					require.NotContains(t, out.String(), hint)
				}
			}
		})
	}
}
