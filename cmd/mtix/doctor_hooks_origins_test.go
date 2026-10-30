// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
)

const originsHooksYAML = "hooks:\n" +
	"  - {name: ok, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'https://93.184.216.34/h'}}\n" +
	"  - {name: fresh, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'https://fresh.example/h'}}\n" +
	"  - {name: loud, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'http://127.0.0.1:7/h'}}\n"

func originsRow(t *testing.T) service.DoctorResult {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		if row.ID == "hooks.origins" {
			return row
		}
	}
	t.Fatal("hooks.origins row missing")
	return service.DoctorResult{}
}

func TestDoctor_OriginsCheck_WarnsWithExactFixes(t *testing.T) {
	state := reviewProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(originsHooksYAML), 0o600))
	_, err := hooks.AllowOrigin(state, "https://93.184.216.34", false, true, false)
	require.NoError(t, err)
	_, err = hooks.AllowOrigin(state, "http://127.0.0.1:7", false, true, false)
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	before, err := os.ReadDir(dir)
	require.NoError(t, err)

	row := originsRow(t)
	require.Equal(t, service.DoctorWarn, row.Status)
	require.Contains(t, row.Detail, "fresh")
	require.Contains(t, row.Detail, "loud")
	require.NotContains(t, row.Detail, "93.184.216.34")
	require.Contains(t, row.Fix, "mtix hooks allow-origin 'https://fresh.example:443'")
	require.Contains(t, row.Fix, "mtix hooks allow-origin --allow-private 'http://127.0.0.1:7'")
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"fix"`)

	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, after, len(before), "plain doctor writes nothing")
}

func TestDoctor_OriginsCheck_PassesWhenEveryOriginIsConsented(t *testing.T) {
	state := reviewProject(t)
	yaml := "hooks:\n  - {name: ok, match: {events: [node.created]}, deliver: [webhook], webhook: {url: 'https://93.184.216.34/h'}}\n"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(yaml), 0o600))
	_, err := hooks.AllowOrigin(state, "https://93.184.216.34", false, true, false)
	require.NoError(t, err)
	require.Equal(t, service.DoctorPass, originsRow(t).Status)
}

func TestDoctor_OriginsCheck_PassesWithNoWebhookHooks(t *testing.T) {
	reviewProject(t)
	require.Equal(t, service.DoctorPass, originsRow(t).Status)
}

func TestDoctor_OriginsCheck_UnreadableStateWarns(t *testing.T) {
	state := reviewProject(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(originsHooksYAML), 0o600))
	_, err := hooks.AllowOrigin(state, "https://93.184.216.34", false, true, false)
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "webhook-origins.json"), []byte("{broken"), 0o600))
	row := originsRow(t)
	require.Equal(t, service.DoctorWarn, row.Status)
	require.Contains(t, row.Detail, "unreadable")
}

func TestDoctor_OriginsCheck_NoRowWithoutHooksFile(t *testing.T) {
	reviewProject(t)
	require.NoError(t, os.Remove(filepath.Join(app.mtixDir, "hooks.yaml")))
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		require.NotEqual(t, "hooks.origins", row.ID)
	}
}

func TestOriginsResult_IdenticalFixes_ListedOnce(t *testing.T) {
	issues := []hooks.OriginIssue{
		{Hook: "a", Reason: "consent state unreadable", Fix: "remove the record, then run X"},
		{Hook: "b", Reason: "consent state unreadable", Fix: "remove the record, then run X"},
		{Hook: "c", Reason: "not consented", Fix: "mtix hooks allow-origin 'https://c.example:443'"},
	}
	row := originsResult(issues)
	require.Equal(t, "remove the record, then run X; mtix hooks allow-origin 'https://c.example:443'", row.Fix)
	require.Contains(t, row.Detail, "a (")
	require.Contains(t, row.Detail, "b (")
}

func TestOriginsResult_HostileValues_RenderedWithoutControlCharacters(t *testing.T) {
	res := originsResult([]hooks.OriginIssue{{
		Hook:   "evil\x1b[31mhook",
		Reason: "bad\nrow\x1b]0;title\x07",
		Fix:    "mtix hooks allow-origin 'https://x.example\x1b[2J'",
	}})
	require.Equal(t, service.DoctorWarn, res.Status)
	for _, text := range []string{res.Detail, res.Fix} {
		for _, r := range text {
			require.False(t, r < 0x20 || r == 0x7f, "control character %q in %q", r, text)
		}
	}
}
