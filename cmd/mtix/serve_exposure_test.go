// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// TestEffectiveServeBind_FlagConfigDefault_FlagWinsThenConfig pins the one
// rule serve and doctor share (MTIX-220): --addr when given, else api.bind,
// else loopback.
func TestEffectiveServeBind_FlagConfigDefault_FlagWinsThenConfig(t *testing.T) {
	cases := []struct {
		name       string
		addr       string
		changed    bool
		configured string
		want       string
	}{
		{"flag given beats config", "0.0.0.0", true, "192.0.2.10", "0.0.0.0"},
		{"flag given as loopback beats a network config", "127.0.0.1", true, "192.0.2.10", "127.0.0.1"},
		{"no flag uses config", "127.0.0.1", false, "192.0.2.10", "192.0.2.10"},
		{"no flag and no config is loopback", "127.0.0.1", false, "", "127.0.0.1"},
		{"flag default value is ignored when not given", "ignored", false, "", "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, effectiveServeBind(tc.addr, tc.changed, tc.configured))
		})
	}
}

// TestPrepareServe_NetworkBindWithoutInsecureBind_RefusesAndTouchesNoState
// pins story 1: the refusal names both requirements and the exact fix, and it
// happens before any token file exists.
func TestPrepareServe_NetworkBindWithoutInsecureBind_RefusesAndTouchesNoState(t *testing.T) {
	state := alertDoctorFixture(t)
	_, err := prepareServe(serveRequest{Addr: "192.0.2.10", AddrChanged: true, Port: 8377})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--insecure-bind")
	assert.Contains(t, err.Error(), "token")
	assert.Contains(t, err.Error(), "mtix serve --addr 192.0.2.10 --insecure-bind")
	_, present, readErr := readServeToken(state)
	require.NoError(t, readErr)
	assert.False(t, present, "a refused start creates no token")
}

// TestPrepareServe_LoopbackBind_NeedsNothing pins that loopback is unchanged:
// no flag, no token, no state.
func TestPrepareServe_LoopbackBind_NeedsNothing(t *testing.T) {
	state := alertDoctorFixture(t)
	for _, addr := range []string{"127.0.0.1", "::1", "localhost"} {
		cfg, err := prepareServe(serveRequest{Addr: addr, AddrChanged: true, Port: 8377})
		require.NoError(t, err, addr)
		assert.Empty(t, cfg.AuthToken)
		assert.False(t, cfg.InsecureBind)
	}
	_, present, err := readServeToken(state)
	require.NoError(t, err)
	assert.False(t, present)
}

// TestPrepareServe_InsecureBind_CreatesTokenOnceAndNeverPrintsIt pins story 5:
// the token is created on first use, reused afterwards, stored at mode 0600,
// and only its path appears in the output.
func TestPrepareServe_InsecureBind_CreatesTokenOnceAndNeverPrintsIt(t *testing.T) {
	state := alertDoctorFixture(t)
	var first, second serverConfigView
	out, err := cliBehaviorOutput(t, func() error {
		cfg, prepErr := prepareServe(serveRequest{Addr: "192.0.2.10", AddrChanged: true, Port: 8377, InsecureBind: true})
		first = serverConfigView{cfg.AuthToken, cfg.InsecureBind, cfg.Bind}
		return prepErr
	})
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{64}$`, first.token, "32 random bytes as hex")
	assert.True(t, first.insecure)
	assert.Equal(t, "192.0.2.10", first.bind)
	assert.NotContains(t, out, first.token, "the token value is never printed")
	assert.Contains(t, out, serveTokenPath(state))
	assert.Contains(t, out, "created")

	out, err = cliBehaviorOutput(t, func() error {
		cfg, prepErr := prepareServe(serveRequest{Addr: "192.0.2.10", AddrChanged: true, Port: 8377, InsecureBind: true})
		second = serverConfigView{cfg.AuthToken, cfg.InsecureBind, cfg.Bind}
		return prepErr
	})
	require.NoError(t, err)
	assert.Equal(t, first.token, second.token, "the existing token is reused")
	assert.NotContains(t, out, first.token)
	assert.Contains(t, out, "using")

	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(serveTokenPath(state))
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

type serverConfigView struct {
	token    string
	insecure bool
	bind     string
}

// TestServeToken_UnsafeOrMalformedRecords_AreRefused pins story 5: a token
// file that is group or world readable, or not a well-formed token, is an
// error, never a silently accepted credential.
func TestServeToken_UnsafeOrMalformedRecords_AreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes")
	}
	state := alertDoctorFixture(t)
	token, created, err := ensureServeToken(state, newServeToken)
	require.NoError(t, err)
	require.True(t, created)
	path := serveTokenPath(state)

	require.NoError(t, os.Chmod(path, 0o644)) //nolint:gosec // the test makes the file unsafe on purpose
	_, _, err = readServeToken(state)
	require.Error(t, err, "world readable")
	require.NoError(t, os.Chmod(path, 0o640)) //nolint:gosec // group readable on purpose
	_, _, err = readServeToken(state)
	require.Error(t, err, "group readable")
	require.NoError(t, os.Chmod(path, 0o600))
	got, present, err := readServeToken(state)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, token, got)

	malformed := func(content string) {
		t.Helper()
		_, _, readErr := ensureServeToken(malformedStore(t, state, content), newServeToken)
		require.Error(t, readErr, content)
	}
	malformed("short")
	malformed("ZZZZ" + token[4:])
	malformed(token + "00")
}

// TestServeToken_ProjectTree_IsRefused pins that a token directory inside a
// project (a git work tree) is refused.
func TestServeToken_ProjectTree_IsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	project, err := os.MkdirTemp(home, ".mtix-serve-project-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(project)) })
	require.NoError(t, os.Mkdir(filepath.Join(project, ".git"), 0o700))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(project, "config"))
	t.Setenv("APPDATA", filepath.Join(project, "config"))
	state, err := operatorState()
	require.NoError(t, err)
	_, _, err = ensureServeToken(state, newServeToken)
	require.Error(t, err, "a token inside a project tree is refused")
}

// TestPrepareServe_ConfiguredBind_IsHonouredAndRefusedWithoutOptIn pins the
// behaviour change of MTIX-220: a configured non-loopback api.bind now
// triggers the same refusal as --addr.
func TestPrepareServe_ConfiguredBind_IsHonouredAndRefusedWithoutOptIn(t *testing.T) {
	alertDoctorFixture(t)
	_, err := app.configSvc.Set("api.bind", "192.0.2.10")
	require.NoError(t, err)
	_, err = prepareServe(serveRequest{Addr: "127.0.0.1", Port: 8377})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mtix serve --addr 192.0.2.10 --insecure-bind")

	cfg, err := prepareServe(serveRequest{Addr: "127.0.0.1", AddrChanged: true, Port: 8377})
	require.NoError(t, err, "an explicit loopback --addr overrides the configured network bind")
	assert.Equal(t, "127.0.0.1", cfg.Bind)
}

// TestServeExposureResult_BindAndTokenStates_ReportsExactFix pins story 6:
// the row text, the exact fix, the machine-readable data and the absence of
// the token.
func TestServeExposureResult_BindAndTokenStates_ReportsExactFix(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	path := "/state/serve-token.json"
	cases := []struct {
		name    string
		bind    string
		token   string
		present bool
		err     error
		status  service.DoctorStatus
		fix     string
	}{
		{"loopback", "127.0.0.1", "", false, nil, service.DoctorPass, ""},
		{"network with token", "192.0.2.10", secret, true, nil, service.DoctorPass, ""},
		{"network without token", "192.0.2.10", "", false, nil, service.DoctorWarn, "run mtix serve --addr 192.0.2.10 --insecure-bind (it creates the token on first use), or set api.bind to 127.0.0.1"},
		{"network with unusable token", "192.0.2.10", "", true, assert.AnError, service.DoctorFail, "chmod 600 " + path},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := serveExposureResult(tc.bind, tc.token, tc.present, path, tc.err)
			assert.Equal(t, tc.status, row.Status)
			assert.Contains(t, row.Fix, tc.fix)
			assert.Equal(t, tc.bind, row.Data["bind"])
			assert.Equal(t, tc.token != "" && tc.err == nil, row.Data["token_present"], "token_present tells a machine reader the truth")
			encoded, err := json.Marshal(row)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), secret, "the token never appears in doctor output")
		})
	}
}

// TestDoctor_ServeExposureRow_FollowsTheConfiguredBind pins that the
// registered doctor row reads the same effective bind as serve: no row for a
// loopback bind, a warning with the exact fix for a configured network bind
// without a token, a pass once the token exists.
func TestDoctor_ServeExposureRow_FollowsTheConfiguredBind(t *testing.T) {
	state := alertDoctorFixture(t)
	run := func() (service.DoctorResult, bool) {
		svc, err := newConfiguredDoctorService()
		require.NoError(t, err)
		rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
		require.NoError(t, err)
		for _, row := range rows {
			if row.ID == serveExposureCheckID {
				return row, true
			}
		}
		return service.DoctorResult{}, false
	}
	_, found := run()
	assert.False(t, found, "a loopback bind adds no row to the default doctor output")

	_, err := app.configSvc.Set("api.bind", "192.0.2.10")
	require.NoError(t, err)
	row, found := run()
	require.True(t, found)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Equal(t, "192.0.2.10", row.Data["bind"])
	assert.Contains(t, row.Fix, "mtix serve --addr 192.0.2.10 --insecure-bind")

	token, _, err := ensureServeToken(state, newServeToken)
	require.NoError(t, err)
	row, found = run()
	require.True(t, found)
	assert.Equal(t, service.DoctorPass, row.Status)
	assert.Equal(t, true, row.Data["token_present"])
	encoded, err := json.Marshal(row)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), token)
}

// malformedStore overwrites the stored token record with content and returns
// the same operator state, so a read sees a record that is not a valid token.
func malformedStore(t *testing.T, state *oplocal.State, content string) *oplocal.State {
	t.Helper()
	require.NoError(t, state.WriteJSON(serveTokenRecord, serveTokenFile{Token: content}))
	return state
}

// TestPrepareServe_AllowOrigin_ValidatedAndPassedOn verifies that
// --allow-origin values reach the server configuration, that the default
// is an empty allow-list, and that an entry no browser could send is
// refused before the server starts.
func TestPrepareServe_AllowOrigin_ValidatedAndPassedOn(t *testing.T) {
	alertDoctorFixture(t)
	tests := []struct {
		name    string
		origins []string
		wantErr bool
	}{
		{"no flag means an empty allow-list", nil, false},
		{"one exact origin", []string{"http://localhost:5173"}, false},
		{"two exact origins", []string{"http://localhost:5173", "https://app.example"}, false},
		{"trailing slash", []string{"http://localhost:5173/"}, true},
		{"wildcard", []string{"*"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := prepareServe(serveRequest{Addr: "127.0.0.1", AddrChanged: true, Port: 8377, AllowedOrigins: tt.origins})
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--allow-origin")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.origins, cfg.AllowedOrigins)
		})
	}
}

// TestServeCmd_AllowOriginFlag_RepeatableAndEmptyByDefault verifies the
// --allow-origin flag: repeatable, no comma splitting, empty by default.
func TestServeCmd_AllowOriginFlag_RepeatableAndEmptyByDefault(t *testing.T) {
	cmd := newServeCmd()
	flag := cmd.Flags().Lookup("allow-origin")
	require.NotNil(t, flag)
	assert.Equal(t, "[]", flag.DefValue)
	require.NoError(t, cmd.Flags().Parse([]string{"--allow-origin", "http://localhost:5173", "--allow-origin", "https://a.example,https://b.example"}))
	got, err := cmd.Flags().GetStringArray("allow-origin")
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:5173", "https://a.example,https://b.example"}, got)
}
