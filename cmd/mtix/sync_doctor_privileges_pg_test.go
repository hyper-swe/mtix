// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// doctorJSON is the --json form of the doctor report, as agents read it.
type doctorJSON struct {
	Pass   bool `json:"pass"`
	Checks []struct {
		Name     string            `json:"name"`
		Pass     bool              `json:"pass"`
		Warn     bool              `json:"warn"`
		Detail   string            `json:"detail"`
		Fix      string            `json:"fix"`
		Findings []json.RawMessage `json:"findings"`
	} `json:"checks"`
}

// runDoctorAs runs `mtix sync doctor --json` connected as role and returns
// the parsed report, the raw output and the error.
func runDoctorAs(t *testing.T, f *hardenFixture, role string) (doctorJSON, map[string]json.RawMessage, error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, f.dsnAs(role))
	app.jsonOutput = true
	var stdout, stderr bytes.Buffer
	err := runSyncDoctor(context.Background(), &stdout, &stderr, nil, transport.Options{InsecureTLS: true})
	var report doctorJSON
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), stdout.String())
	var raw struct {
		Checks []map[string]json.RawMessage `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &raw))
	for _, c := range raw.Checks {
		if string(c["name"]) == `"hub-privileges"` {
			return report, c, err
		}
	}
	t.Fatalf("no hub-privileges check in:\n%s", stdout.String())
	return report, nil, err
}

// hubPrivilegesCheck returns the hub-privileges check of report.
func hubPrivilegesCheck(t *testing.T, report doctorJSON) (pass, warn bool, detail, fix string, findings int) {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == "hub-privileges" {
			return c.Pass, c.Warn, c.Detail, c.Fix, len(c.Findings)
		}
	}
	t.Fatal("no hub-privileges check")
	return false, false, "", "", 0
}

// setKeepRoles records sync.keep_roles in the test project's config, which
// turns on strict mode.
func setKeepRoles(t *testing.T, value string) {
	t.Helper()
	_, err := app.configSvc.Set("sync.keep_roles", value)
	require.NoError(t, err)
}

// TestDoctorHubPrivileges_DefaultMode_WarnsAndExitsZero: with no role
// configured, access by roles other than the owner is a WARN: the check
// passes with warn set, the doctor exits 0, and the detail says who can
// use the sync tables, that this may be fine on a private network, and how
// to restrict it; --json carries name, pass, warn, detail, fix and
// findings (MTIX-95.1).
func TestDoctorHubPrivileges_DefaultMode_WarnsAndExitsZero(t *testing.T) {
	h := newExposedHub(t, true)
	report, raw, err := runDoctorAs(t, h.f, h.owner)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	require.True(t, report.Pass)

	pass, warn, detail, fix, findings := hubPrivilegesCheck(t, report)
	require.True(t, pass)
	require.True(t, warn)
	for _, want := range []string{h.anon, h.unrel, "PUBLIC", "private network", "mtix sync harden"} {
		require.Contains(t, detail, want)
	}
	require.Equal(t, "mtix sync harden", fix)
	require.Positive(t, findings)
	for _, key := range []string{"name", "pass", "warn", "detail", "fix", "findings"} {
		require.Contains(t, raw, key, "--json carries %s", key)
	}
}

// TestDoctorHubPrivileges_StrictMode_FailsOnUnlistedRole: with
// sync.keep_roles set, a role not in it that can use the sync tables fails
// the check and the doctor exits 2 (MTIX-95.1).
func TestDoctorHubPrivileges_StrictMode_FailsOnUnlistedRole(t *testing.T) {
	h := newExposedHub(t, true)
	setKeepRoles(t, h.team)
	report, _, err := runDoctorAs(t, h.f, h.team)
	require.ErrorIs(t, err, errDoctorChecksFailed)
	require.False(t, report.Pass)
	pass, warn, detail, _, _ := hubPrivilegesCheck(t, report)
	require.False(t, pass)
	require.False(t, warn)
	require.Contains(t, detail, "sync.keep_roles")
	require.Contains(t, detail, h.anon)
	start := strings.Index(detail, "can reach them: ")
	end := strings.Index(detail, "; kept roles that can grant")
	require.True(t, start >= 0 && end > start, detail)
	require.NotContains(t, detail[start:end], h.team, "the kept role is not listed among the other roles")
	require.Contains(t, detail, "kept roles that can grant their access on, or hold it by another role's grant: "+h.team)
}

// TestDoctorHubPrivileges_GuardMissingOrDisabled_WarnsOrFailsStrict covers
// a TRUNCATE guard that is missing or disabled: a WARN by default, a FAIL
// in strict mode (MTIX-95.1).
func TestDoctorHubPrivileges_GuardMissingOrDisabled_WarnsOrFailsStrict(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := map[bool]string{false: "default mode warns", true: "strict mode fails"}[strict]
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			f := newHardenFixture(t)
			owner, team := f.ownerRole(), f.role("team")
			f.migrateAs(owner)
			f.exec(`ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_truncate`)
			f.exec(`DROP TRIGGER sync_events_no_truncate ON sync_events`)
			if strict {
				setKeepRoles(t, team)
			}
			report, _, err := runDoctorAs(t, f, owner)
			pass, warn, detail, _, _ := hubPrivilegesCheck(t, report)
			require.Contains(t, detail, "TRUNCATE guard")
			if strict {
				require.ErrorIs(t, err, errDoctorChecksFailed)
				require.False(t, pass)
				return
			}
			require.NoError(t, err)
			require.True(t, pass)
			require.True(t, warn)
		})
	}
}

// TestDoctorHubPrivileges_CleanHub_Passes: a freshly migrated hub passes
// without a warning, and after `harden --apply` an exposed hub passes too,
// in default and strict mode (MTIX-95.1).
func TestDoctorHubPrivileges_CleanHub_Passes(t *testing.T) {
	initTestApp(t)
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner)
	report, raw, err := runDoctorAs(t, f, owner)
	require.NoError(t, err)
	pass, warn, _, _, findings := hubPrivilegesCheck(t, report)
	require.True(t, pass)
	require.False(t, warn)
	require.Zero(t, findings)
	require.NotContains(t, raw, "warn", "warn is omitted when false")

	h := newExposedHub(t, true)
	_, err = h.f.harden(h.owner, "--apply", "--keep-role", h.team)
	require.NoError(t, err)
	setKeepRoles(t, h.team)
	report, _, err = runDoctorAs(t, h.f, h.owner)
	require.NoError(t, err)
	pass, warn, _, _, _ = hubPrivilegesCheck(t, report)
	require.True(t, pass)
	require.False(t, warn)
}

// TestDoctorHubPrivileges_VerificationError_WarnsOrFailsStrict: when the
// verification itself cannot run (here a sync table other than
// sync_projects is missing, so "schema current" still passes), the check
// is a WARN by default that says so and names the next step, and the
// doctor exits 0; in strict mode it fails (MTIX-95.1).
func TestDoctorHubPrivileges_VerificationError_WarnsOrFailsStrict(t *testing.T) {
	for _, strict := range []bool{false, true} {
		name := map[bool]string{false: "default mode warns", true: "strict mode fails"}[strict]
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			f := newHardenFixture(t)
			owner, team := f.ownerRole(), f.role("team")
			f.migrateAs(owner)
			f.exec(`DROP TABLE node_renumber_remaps`)
			if strict {
				setKeepRoles(t, team)
			}
			report, _, err := runDoctorAs(t, f, owner)
			pass, warn, detail, _, _ := hubPrivilegesCheck(t, report)
			require.Contains(t, detail, "could not verify hub privileges")
			require.Contains(t, detail, "node_renumber_remaps is missing")
			require.Contains(t, detail, "check the hub connection")
			require.Contains(t, detail, "then run mtix sync doctor again")
			if strict {
				require.ErrorIs(t, err, errDoctorChecksFailed)
				require.False(t, pass)
				return
			}
			require.NoError(t, err, "never red by default")
			require.True(t, pass)
			require.True(t, warn)
		})
	}
}
