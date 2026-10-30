// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestNormalizeOrigin_Inputs_CanonicalOrError(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"https default port", "https://Hooks.Example/path?q=1", "https://hooks.example:443", false},
		{"http default port", "http://hooks.example", "http://hooks.example:80", false},
		{"explicit port", "https://hooks.example:8443/x", "https://hooks.example:8443", false},
		{"ipv6 literal", "http://[::1]:9000/x", "http://[::1]:9000", false},
		{"trailing dot", "https://hooks.example./x", "https://hooks.example:443", false},
		{"userinfo refused", "https://user:pw@hooks.example/x", "", true},
		{"ftp refused", "ftp://hooks.example/x", "", true},
		{"no host", "https:///x", "", true},
		{"empty", "", "", true},
		{"relative", "/just/a/path", "", true},
		{"zoned ipv6", "http://[fe80::1%25en0]:8080/x", "", true},
		{"double trailing dot", "http://a..:80/x", "", true},
		{"dot only host", "http://./x", "", true},
		{"encoded host", "https://%41.Example/x", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeOrigin(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIsBlockedAddress_Classes_RefusedUnlessPublic(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true}, {"::1", true}, {"10.1.2.3", true}, {"172.16.0.1", true},
		{"192.168.1.1", true}, {"169.254.169.254", true}, {"fe80::1", true}, {"fc00::1", true},
		{"100.64.0.1", true}, {"0.0.0.0", true}, {"::", true}, {"224.0.0.1", true},
		{"::ffff:10.0.0.1", true}, {"::ffff:127.0.0.1", true}, {"64:ff9b::7f00:1", true},
		{"::127.0.0.1", true}, {"fec0::1", true}, {"2001:0:4136:e378:8000:63bf:3fff:fdd2", true},
		{"93.184.216.34", false}, {"2606:2800:220:1:248:1893:25c8:1946", false},
	}
	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			require.Equal(t, tt.blocked, IsBlockedAddress(tt.ip))
		})
	}
}

func TestCheckDialAddress_ActualAddress_Enforced(t *testing.T) {
	require.Error(t, checkDialAddress("127.0.0.1:80", false))
	require.Error(t, checkDialAddress("[::1]:80", false))
	require.Error(t, checkDialAddress("169.254.169.254:80", false))
	require.Error(t, checkDialAddress("not-an-address", false), "unparseable fails closed")
	require.NoError(t, checkDialAddress("127.0.0.1:80", true), "explicitly listed")
	require.NoError(t, checkDialAddress("93.184.216.34:443", false))
	var refused *OriginRefusedError
	require.True(t, errors.As(checkDialAddress("10.0.0.1:1", false), &refused))
}

func TestAllowOrigin_Gates_RefusedWithoutTerminalOrWithSession(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := AllowOrigin(state, "https://hooks.example/x", false, false, false)
	require.Error(t, err, "no terminal")
	_, err = AllowOrigin(state, "https://hooks.example/x", false, true, true)
	require.Error(t, err, "agent session marker")
	list, err := ReadOrigins(state)
	require.NoError(t, err)
	require.Empty(t, list.Origins, "nothing written on refusal")
}

func TestAllowOrigin_Terminal_StoresNormalizedAndIdempotent(t *testing.T) {
	state := testutil.OperatorState(t)
	got, err := AllowOrigin(state, "https://Hooks.Example/x", false, true, false)
	require.NoError(t, err)
	require.Equal(t, "https://hooks.example:443", got.Origin)
	_, err = AllowOrigin(state, "https://hooks.example:443/y", false, true, false)
	require.NoError(t, err)
	list, err := ReadOrigins(state)
	require.NoError(t, err)
	require.Len(t, list.Origins, 1)
	_, err = AllowOrigin(state, "https://hooks.example/", true, true, false)
	require.NoError(t, err)
	list, err = ReadOrigins(state)
	require.NoError(t, err)
	require.Len(t, list.Origins, 1)
	require.True(t, list.Origins[0].AllowPrivate, "re-listing updates the private flag")
	_, err = AllowOrigin(state, "ftp://x", false, true, false)
	require.Error(t, err)
}

func TestAllowOrigin_Limit_RefusesBeyondCap(t *testing.T) {
	state := testutil.OperatorState(t)
	for i := 0; i < maxConsentedOrigins; i++ {
		_, err := AllowOrigin(state, "https://h"+strconv.Itoa(i)+".example", false, true, false)
		require.NoError(t, err)
	}
	_, err := AllowOrigin(state, "https://overflow.example", false, true, false)
	require.Error(t, err)
}

func TestWriteOrigins_File_PrivateAndNoTempLeft(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	state := testutil.OperatorState(t)
	_, err := AllowOrigin(state, "https://hooks.example", false, true, false)
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, webhookOriginsRecord+".json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), "tmp", "atomic write leaves no temporary file")
	}
}

func TestReadOrigins_Corrupt_FailsClosed(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := AllowOrigin(state, "https://hooks.example", false, true, false)
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, webhookOriginsRecord+".json"), []byte("{broken"), 0o600))
	_, err = ReadOrigins(state)
	require.Error(t, err)

	src := StateOriginSource(state)
	_, err = src()
	require.Error(t, err)
}

func TestReadOrigins_Missing_IsEmpty(t *testing.T) {
	list, err := ReadOrigins(testutil.OperatorState(t))
	require.NoError(t, err)
	require.Empty(t, list.Origins)
}

func TestReadOrigins_UnreadableState_FailsClosed(t *testing.T) {
	_, err := ReadOrigins(nil)
	require.Error(t, err, "nil state denies")
}

func TestWebhookOriginIssues_Config_ReportsFixes(t *testing.T) {
	cfg := Config{Hooks: []Hook{
		{Name: "ok", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "https://ok.example/h"}},
		{Name: "new", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "https://new.example/h"}},
		{Name: "lo", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "http://127.0.0.1:9/h"}},
		{Name: "lo-ok", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "http://127.0.0.1:10/h"}},
		{Name: "lo-nopriv", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "http://127.0.0.1:11/h"}},
		{Name: "named-local", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "http://localhost:12/h"}},
		{Name: "bad", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "ftp://x"}},
		{Name: "exec", Deliver: []string{AdapterExec}},
	}}
	list := OriginList{Origins: []Origin{
		{Origin: "https://ok.example:443"},
		{Origin: "http://127.0.0.1:10", AllowPrivate: true},
		{Origin: "http://127.0.0.1:11"},
		{Origin: "http://localhost:12"},
	}}
	issues := webhookOriginIssues(list, nil, cfg, res(fixedLookup("192.0.2.1", nil)))
	byHook := map[string]OriginIssue{}
	for _, i := range issues {
		byHook[i.Hook] = i
	}
	require.Len(t, issues, 5)
	require.Contains(t, byHook["lo-nopriv"].Reason, "not explicitly allowed", "consented loopback without the private opt-in")
	require.Equal(t, "mtix hooks allow-origin --allow-private 'http://127.0.0.1:11'", byHook["lo-nopriv"].Fix)
	require.Contains(t, byHook["named-local"].Fix, "--allow-private", "localhost names are private without a DNS lookup")
	require.Contains(t, byHook["new"].Fix, "mtix hooks allow-origin 'https://new.example:443'")
	require.Contains(t, byHook["lo"].Fix, "--allow-private")
	require.Contains(t, byHook["bad"].Reason, "invalid")

	failed := WebhookOriginIssues(OriginList{}, errors.New("unreadable"), cfg)
	require.NotEmpty(t, failed)
	require.Contains(t, failed[0].Fix, "mtix hooks status --json")
}

func TestOriginAllowFix_QuoteInOrigin_RoundTripsThroughShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	const hostile = "https://a'$(touch pwned);b.example:443"
	fix := OriginAllowFix(hostile, false)
	out, err := exec.Command("sh", "-c", "set -- "+strings.TrimPrefix(fix, "mtix hooks allow-origin")+"; printf %s \"$1\"").Output()
	require.NoError(t, err)
	require.Equal(t, hostile, string(out))
}

func TestReadOrigins_ForeignHostRecord_FailsClosedAndRefusesDelivery(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := AllowOrigin(state, "https://hooks.example", false, true, false)
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	file := filepath.Join(dir, webhookOriginsRecord+".json")
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	var rec map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &rec))
	rec["host_id"] = json.RawMessage(`"` + strings.Repeat("0", 32) + `"`)
	raw, err = json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0o600))

	_, err = ReadOrigins(state)
	require.Error(t, err)
	err = NewWebhookAdapter(StateOriginSource(state)).Deliver(context.Background(), webhookDelivery("https://hooks.example/h", []byte(`{}`)))
	var refused *OriginRefusedError
	require.True(t, errors.As(err, &refused))
}

func TestReadOrigins_InvalidEntries_FailClosed(t *testing.T) {
	state := testutil.OperatorState(t)
	require.NoError(t, state.WriteJSON(webhookOriginsRecord, OriginList{Origins: []Origin{{Origin: "https://Hooks.Example/x"}}}))
	_, err := ReadOrigins(state)
	require.Error(t, err, "an entry that is not in canonical form")
	many := make([]Origin, maxConsentedOrigins+1)
	for i := range many {
		many[i] = Origin{Origin: "https://h" + strconv.Itoa(i) + ".example:443"}
	}
	require.NoError(t, state.WriteJSON(webhookOriginsRecord, OriginList{Origins: many}))
	_, err = ReadOrigins(state)
	require.Error(t, err, "over the cap")
}

func TestNormalizeOrigin_Idempotent_AndAlwaysStorable(t *testing.T) {
	inputs := []string{
		"http://[fe80::1%25en0]:8080", "http://a..:80", "http://a.:80", "HTTPS://Hooks.Example:443/x?y#z",
		"http://[::1]", "http://[0:0:0:0:0:0:0:1]:80", "http://EXAMPLE.com.:8080", "https://%41.example",
		"http://127.0.0.1:0", "http://a b.example", "http://xn--bcher-kva.example", "http://bücher.example",
		"http://[::ffff:10.0.0.1]:80", "http://1.2.3.4.:80", "http://a%2e.example", "https://h.example:00443",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			first, err := NormalizeOrigin(in)
			if err != nil {
				return // refusing is allowed; storing a non-canonical value is not
			}
			second, err := NormalizeOrigin(first)
			require.NoError(t, err, "a normalized origin must normalize again")
			require.Equal(t, first, second)

			state := testutil.OperatorState(t)
			entry, err := AllowOrigin(state, in, false, true, false)
			require.NoError(t, err)
			list, err := ReadOrigins(state)
			require.NoError(t, err, "anything AllowOrigin stored is readable")
			require.Equal(t, []Origin{entry}, list.Origins)
		})
	}
}

func TestAllowOrigin_NonCanonicalInput_RefusedAndStoreStaysReadable(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := AllowOrigin(state, "https://ok.example", false, true, false)
	require.NoError(t, err)
	for _, in := range []string{"http://[fe80::1%25en0]:8080", "http://a..:80"} {
		_, err = AllowOrigin(state, in, false, true, false)
		require.Error(t, err, in)
	}
	list, err := ReadOrigins(state)
	require.NoError(t, err)
	require.Len(t, list.Origins, 1)
}

func TestWebhookOriginIssues_UnreadableState_NamesRecordAndResetStep(t *testing.T) {
	testutil.OperatorState(t)
	cfg := Config{Hooks: []Hook{{Name: "w", Deliver: []string{AdapterWebhook}, Webhook: &WebhookConfig{URL: "https://w.example/h"}}}}
	issues := WebhookOriginIssues(OriginList{}, errors.New("corrupt"), cfg)
	require.Len(t, issues, 1)
	require.Contains(t, issues[0].Fix, webhookOriginsRecord+".json")
	require.Contains(t, issues[0].Fix, "remove")
	require.Contains(t, issues[0].Fix, "mtix hooks allow-origin")
	require.NotEqual(t, "mtix hooks status --json", issues[0].Fix, "no circular fix")
}
