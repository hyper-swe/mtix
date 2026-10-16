// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// recordingAdapter is a test-only harness adapter; production adapters arrive
// with the harness tickets, so none is registered by this build.
type recordingAdapter struct {
	calls []attach.VerbRequest
	err   error
}

func (a *recordingAdapter) Run(_ context.Context, req attach.VerbRequest) error {
	a.calls = append(a.calls, req)
	return a.err
}

// testGlueWriter stands in for a harness glue writer in cmd tests only.
type testGlueWriter struct {
	harness string
	writes  int
}

func (w *testGlueWriter) Name() string { return "glue." + w.harness }
func (w *testGlueWriter) Check(context.Context) (attach.ArtifactStatus, error) {
	return attach.ArtifactStatus{State: attach.ArtifactCurrent}, nil
}
func (w *testGlueWriter) Write(context.Context) error {
	w.writes++
	return nil
}

var attachTestNow = time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)

func runAttachVerbForTest(t *testing.T, deps attachDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newAttachCommand(deps)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("")) // never a terminal
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func attachTestDeps(st *oplocal.State, current int, adapters map[string]attach.Adapter) attachDeps {
	return attachDeps{
		current:  current,
		adapters: adapters,
		state:    func() (*oplocal.State, error) { return st, nil },
		clock:    func() time.Time { return attachTestNow },
	}
}

func attachAlerts(t *testing.T, st *oplocal.State) []oplocal.Alert {
	t.Helper()
	rows, err := service.NewAlerts(st, func() time.Time { return attachTestNow }).Recent(context.Background(), 0)
	require.NoError(t, err)
	return rows
}

func TestAttachHandshake_NMinus1_ServedAndMarkedForRefresh(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})

	_, stderr, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "claude-code", "--protocol", "1")
	require.NoError(t, err)
	assert.Empty(t, stderr, "N-1 is served quietly")
	require.Len(t, adapter.calls, 1)
	assert.Equal(t, 1, adapter.calls[0].Protocol, "the old behaviour is served")
	assert.Equal(t, "session-start", adapter.calls[0].Verb)

	pending, err := attach.PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"claude-code": 1}, pending, "the glue is marked for refresh")

	// The refresh needs no terminal: a registry pass (the next command's
	// post-run, or doctor --fix) rewrites the glue and clears the mark.
	glue := &testGlueWriter{harness: "claude-code"}
	reg, err := attach.NewRegistry(2, glue)
	require.NoError(t, err)
	report, err := reg.Refresh(context.Background(), st)
	require.NoError(t, err)
	assert.Equal(t, []string{"glue.claude-code"}, report.Rewritten)
	assert.Equal(t, 1, glue.writes)
	pending, err = attach.PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.Empty(t, attachAlerts(t, st), "serving N-1 is not a failure")
}

func TestAttachHandshake_OutOfRange_InboxOnlyAndAlert(t *testing.T) {
	tests := []struct {
		name     string
		current  int
		protocol string
		writer   bool
		wantFix  string
	}{
		{"older than N-1 without a glue writer", 3, "1", false, "correct or remove the mtix attach entries in the codex configuration"},
		{"older than N-1 with a glue writer", 3, "1", true, "mtix doctor --fix"},
		{"newer than this binary", 2, "3", false, "upgrade mtix"},
		{"protocol zero never existed", attach.Protocol, "0", false, "correct or remove the mtix attach entries in the codex configuration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			adapter := &recordingAdapter{}
			deps := attachTestDeps(st, tt.current, map[string]attach.Adapter{"codex": adapter})
			deps.glueWriter = func(h string) bool { return tt.writer && h == "codex" }
			stdout, stderr, err := runAttachVerbForTest(t, deps, "prompt-submit", "--harness", "codex", "--protocol", tt.protocol)
			require.NoError(t, err, "hook verbs always exit 0")
			assert.Empty(t, adapter.calls, "inbox-only: the adapter never runs")
			assert.Empty(t, stdout)
			assert.Equal(t, 1, strings.Count(stderr, "\n"), "exactly one line: %q", stderr)
			assert.Contains(t, stderr, tt.wantFix, "the line names the fix that really applies")
			assert.Contains(t, stderr, "glue.codex")
			assert.Contains(t, stderr, "inbox")
			assert.NotContains(t, stderr, "mtix setup")
			alerts := attachAlerts(t, st)
			require.Len(t, alerts, 1)
			assert.Equal(t, attachAlertProtocolSkew, alerts[0].Class)
			assert.Equal(t, "codex", alerts[0].Agent)
			assert.Contains(t, alerts[0].Fix, tt.wantFix)
			if !tt.writer {
				assert.NotContains(t, alerts[0].Fix, "doctor --fix", "doctor --fix cannot repair glue without a writer")
			}
		})
	}
}

func TestAttachVerb_UnknownVerb_OneLineAlertNoStdout(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantAgent string
	}{
		{"unknown verb with glue flags", []string{"frobnicate", "--harness", "codex", "--protocol", "2", "--future", "x"}, "codex"},
		{"unknown verb without flags", []string{"frobnicate"}, attachAlertAgent},
		{"no verb", nil, attachAlertAgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			stdout, stderr, err := runAttachVerbForTest(t, attachTestDeps(st, 2, map[string]attach.Adapter{}), tt.args...)
			require.NoError(t, err)
			assert.Empty(t, stdout, "a hook's stdout can reach the agent; nothing is printed there")
			assert.Equal(t, 1, strings.Count(stderr, "\n"), "one line: %q", stderr)
			assert.Contains(t, stderr, "attach verb")
			alerts := attachAlerts(t, st)
			require.Len(t, alerts, 1)
			assert.Equal(t, attachAlertVerbFailure, alerts[0].Class)
			assert.Equal(t, tt.wantAgent, alerts[0].Agent)
			assert.Contains(t, alerts[0].Fix, "upgrade mtix")
		})
	}
}

func TestAttachVerb_Failures_ExitZeroAndAlert(t *testing.T) {
	boom := errors.New("adapter socket closed")
	tests := []struct {
		name     string
		adapters map[string]attach.Adapter
		args     []string
		want     string
		fix      string
	}{
		{"adapter fails", map[string]attach.Adapter{"pi": &recordingAdapter{err: boom}}, []string{"wait", "--harness", "pi", "--protocol", "2"}, "adapter socket closed", "mtix inbox"},
		{"no adapter in this build", map[string]attach.Adapter{}, []string{"session-end", "--harness", "pi", "--protocol", "2"}, "no pi adapter", "mtix version"},
		{"protocol is not a number", map[string]attach.Adapter{}, []string{"session-start", "--harness", "pi", "--protocol", "two"}, "protocol", "harness configuration"},
		{"protocol missing", map[string]attach.Adapter{}, []string{"session-start", "--harness", "pi"}, "protocol", "harness configuration"},
		{"unknown harness", map[string]attach.Adapter{}, []string{"session-start", "--harness", "vim", "--protocol", "2"}, "harness", "harness configuration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			_, stderr, err := runAttachVerbForTest(t, attachTestDeps(st, 2, tt.adapters), tt.args...)
			require.NoError(t, err, "a failing hook verb still exits 0")
			assert.Contains(t, stderr, tt.want)
			assert.NotContains(t, stderr, "mtix setup")
			alerts := attachAlerts(t, st)
			require.Len(t, alerts, 1, "every failure raises an operator alert")
			assert.Equal(t, attachAlertVerbFailure, alerts[0].Class)
			assert.Contains(t, alerts[0].Fix, tt.fix, "each failure names its own remedy")
			assert.NotEqual(t, "mtix doctor", alerts[0].Fix, "a circular fix is not a fix")
		})
	}
}

func TestAttachVerb_AlertUnwritable_StillExitZeroAndSaysSo(t *testing.T) {
	deps := attachDeps{
		current: 2, adapters: map[string]attach.Adapter{},
		state: func() (*oplocal.State, error) { return nil, errors.New("operator state unavailable") },
		clock: func() time.Time { return attachTestNow },
	}
	_, stderr, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "codex", "--protocol", "2")
	require.NoError(t, err)
	assert.Contains(t, stderr, "no codex adapter")
	assert.Contains(t, stderr, "could not record an operator alert")
}

func TestAttachVerb_UnknownFlagsAndArgs_Tolerated(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"codex": adapter})
	_, stderr, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "codex", "--protocol", "2", "--future-flag", "x", "extra")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	require.Len(t, adapter.calls, 1)
}

func TestAttachCommand_ProductionBuild_HiddenNoAdapterAlertsExitZero(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	t.Chdir(t.TempDir()) // no .mtix: attach verbs never open a store
	root := newRootCmd()
	attachCmd, _, err := root.Find([]string{"attach"})
	require.NoError(t, err)
	assert.True(t, attachCmd.Hidden)
	var errOut bytes.Buffer
	root.SetErr(&errOut)
	root.SetArgs([]string{"attach", "session-start", "--harness", "claude-code", "--protocol", "1"})
	require.NoError(t, root.ExecuteContext(context.Background()))
	assert.Contains(t, errOut.String(), "no claude-code adapter")
	alerts := attachAlerts(t, st)
	require.Len(t, alerts, 1)
	assert.Equal(t, attachAlertVerbFailure, alerts[0].Class)
}

func TestAttachVerb_FlagParseErrors_ExitZeroAndAlert(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"protocol without a value", []string{"attach", "session-start", "--harness", "pi", "--protocol"}},
		{"harness without a value", []string{"attach", "session-start", "--protocol", "1", "--harness"}},
		{"malformed root flag", []string{"attach", "session-start", "--harness", "pi", "--protocol", "1", "--json=x"}},
		{"project directory missing", []string{"-C", "/nonexistent/mtix-attach-test", "attach", "session-start", "--harness", "pi", "--protocol", "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveAndResetApp(t)
			st := testutil.OperatorState(t)
			t.Chdir(t.TempDir())
			root := newRootCmd()
			var errOut bytes.Buffer
			root.SetErr(&errOut)
			root.SetOut(&bytes.Buffer{})
			root.SetArgs(tt.args)
			require.NoError(t, root.ExecuteContext(context.Background()), "hook verbs always exit 0")
			assert.Equal(t, 1, strings.Count(errOut.String(), "\n"), "one line; the verb does not also run: %q", errOut.String())
			alerts := attachAlerts(t, st)
			require.Len(t, alerts, 1, "one failure, one alert: the verb does not also run")
			assert.Equal(t, attachAlertVerbFailure, alerts[0].Class)
			assert.Contains(t, alerts[0].Fix, "harness configuration")
		})
	}
}

func TestAttachHandshake_NMinus1MarkFails_AlertAndStillServed(t *testing.T) {
	st := testutil.OperatorState(t)
	require.NoError(t, st.Ensure())
	dir, err := st.Path()
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "attach-refresh.json"), 0o700)) // an unreadable marker record
	adapter := &recordingAdapter{}
	_, stderr, err := runAttachVerbForTest(t, attachTestDeps(st, 2, map[string]attach.Adapter{"codex": adapter}), "session-start", "--harness", "codex", "--protocol", "1")
	require.NoError(t, err)
	require.Len(t, adapter.calls, 1, "N-1 is still served")
	assert.Contains(t, stderr, "could not mark")
	alerts := attachAlerts(t, st)
	require.Len(t, alerts, 1)
	assert.Equal(t, attachAlertVerbFailure, alerts[0].Class)
	assert.Equal(t, operatorStateFix, alerts[0].Fix)
}

func TestAttachHandshake_NegativeProtocol_OneLineOnly(t *testing.T) {
	st := testutil.OperatorState(t)
	_, stderr, err := runAttachVerbForTest(t, attachTestDeps(st, 2, map[string]attach.Adapter{}), "session-start", "--harness", "codex", "--protocol", "-3")
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(stderr, "\n"), stderr)
}
