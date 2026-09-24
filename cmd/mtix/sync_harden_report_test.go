// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
				Via: transport.FindingViaMissing, Fix: "run migration 016_append_only_truncate_guard.sql (creates each missing guard)"},
			{Role: "anon", Object: "default privileges of owner in schema public on tables",
				Kind: transport.FindingKindDefaultACL, Privileges: []string{"SELECT"},
				Via: transport.FindingViaDefaultACL,
				Fix: "ALTER DEFAULT PRIVILEGES FOR ROLE owner IN SCHEMA public REVOKE ALL ON TABLES FROM anon CASCADE"},
		},
		Info: []transport.PrivilegeFinding{
			{Role: "anon", Object: "default privileges of postgres in all schemas on tables",
				Kind: transport.FindingKindDefaultACL, Privileges: []string{"SELECT"}, Via: transport.FindingViaDefaultACL},
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
		"Not changed (default privileges of other roles",
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
			[]string{"verification failed: access remains.", "Access that remains:",
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
