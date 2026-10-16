// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Command scopes and completion notices preserve CLI output contracts.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// New commands must declare their semantics; unknown commands remain unmarked.
func TestCommandFreshness_RegisteredCommandsHaveDeclaredScope(t *testing.T) {
	saveAndResetApp(t)
	count := 0
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.Runnable() {
			count++
			assert.NotEqual(t, freshnessUnmarked, commandFreshness(cmd, nil), cmd.CommandPath())
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(newRootCmd())
	require.Positive(t, count)
	unknown := &cobra.Command{Use: "future-output"}
	require.Equal(t, freshnessUnmarked, commandFreshness(unknown, nil))
}

func TestCommandFreshness_ParsedMixedModes(t *testing.T) {
	for _, tc := range []struct {
		path string
		args []string
		want freshnessScope
	}{
		{"hooks exec-dispatch", nil, freshnessRead},
		{"hooks exec-dispatch", []string{"off"}, freshnessWrite},
		{"hooks trust", []string{"sample", "--status"}, freshnessRead},
		{"hooks trust", []string{"sample"}, freshnessWrite},
		{"doctor", nil, freshnessRead},
		{"doctor", []string{"--fix"}, freshnessWrite},
		{"sync", nil, freshnessRead},
		{"sync", []string{"--fix"}, freshnessWrite},
		{"sync harden", nil, freshnessRead},
		{"sync harden", []string{"--apply"}, freshnessWrite},
		{"agent allow", nil, freshnessWrite},
		{"agent allow", []string{"--yes"}, freshnessWrite},
		{"agent deny", nil, freshnessWrite},
		{"agent deny", []string{"--yes"}, freshnessWrite},
		{"agent permissions recover", nil, freshnessRead},
		{"agent permissions recover", []string{"--cancel", "batch-id"}, freshnessWrite},
		{"agent permissions recover", []string{"--cancel", ""}, freshnessRead},
		{"attach", nil, freshnessProtocol},
		{"attach prompt-submit", nil, freshnessProtocol},
		{"attach session-end", nil, freshnessProtocol},
		{"attach session-start", nil, freshnessProtocol},
		{"attach wait", nil, freshnessProtocol},
		{"daemon", nil, freshnessProtocol},
		{"daemon", []string{"--install"}, freshnessWrite},
		{"sync daemon", nil, freshnessProtocol},
		{"sync daemon", []string{"--install"}, freshnessWrite},
	} {
		t.Run(tc.path+"/"+strings.Join(tc.args, "_"), func(t *testing.T) {
			saveAndResetApp(t)
			root := newRootCmd()
			cmd, _, err := root.Find(strings.Fields(tc.path))
			require.NoError(t, err)
			require.NoError(t, cmd.ParseFlags(tc.args))
			require.Equal(t, tc.want, commandFreshness(cmd, cmd.Flags().Args()))
		})
	}
}

type freshnessCompletionCase struct {
	name                      string
	scope                     freshnessScope
	busy, routed, fail, reset bool
	want                      int
}

func TestFreshnessCompletion_PreservesHandlerResultAndLifecycle(t *testing.T) {
	for _, tc := range []freshnessCompletionCase{
		{"busy-success", freshnessWrite, true, false, false, false, 1},
		{"fresh-success", freshnessWrite, false, false, false, false, 0},
		{"failed-write", freshnessWrite, true, false, true, false, 0},
		{"routed-write", freshnessWrite, true, true, false, false, 0},
		{"cached-read", freshnessRead, true, false, false, false, 0},
		{"unknown-command", freshnessUnmarked, true, false, false, false, 0},
		{"protocol", freshnessProtocol, true, false, false, false, 0},
		{"handler-resets-app", freshnessWrite, true, false, false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { checkFreshnessCompletion(t, tc) })
	}
}

func checkFreshnessCompletion(t *testing.T, tc freshnessCompletionCase) {
	t.Helper()
	saveAndResetApp(t)
	app.outputScope = tc.scope
	if tc.busy {
		app.staleReason = "sync_lock_busy"
	}
	var notice bytes.Buffer
	sentinel, calls := errors.New("handler failed"), 0
	root := &cobra.Command{Use: "mtix"}
	cmd := &cobra.Command{Use: "create", RunE: func(*cobra.Command, []string) error {
		calls++
		assert.NotContains(t, notice.String(), "write succeeded", "completion cannot precede the handler")
		if tc.reset {
			app = appContext{}
		}
		if tc.fail {
			return sentinel
		}
		return nil
	}}
	root.AddCommand(cmd)
	root.SetErr(&notice)
	cmd.SetContext(context.Background())
	if tc.routed {
		cmd.SetContext(withRouted(cmd.Context()))
	}
	wrapFreshnessCompletion(root)
	err := cmd.RunE(cmd, nil)
	if tc.fail {
		require.ErrorIs(t, err, sentinel)
	} else {
		require.NoError(t, err)
	}
	require.Equal(t, 1, calls)
	require.Equal(t, tc.want, strings.Count(notice.String(), "write succeeded"))
	if tc.want > 0 {
		require.Contains(t, notice.String(), "do not repeat")
	}
}

type freshnessNoticeFailure struct{ err error }

func (w freshnessNoticeFailure) Write([]byte) (int, error) { return 0, w.err }

// A notice error cannot make a completed mutation look like a failed write.
func TestFreshnessCompletion_NoticeFailureIsAdvisory(t *testing.T) {
	for _, logging := range []bool{false, true} {
		t.Run(strconv.FormatBool(logging), func(t *testing.T) {
			saveAndResetApp(t)
			app.outputScope, app.staleReason = freshnessWrite, "sync_lock_busy"
			var logs bytes.Buffer
			if logging {
				app.logger = slog.New(slog.NewTextHandler(&logs, nil))
			}
			root := &cobra.Command{Use: "mtix"}
			cmd := &cobra.Command{Use: "create", RunE: func(*cobra.Command, []string) error { return nil }}
			root.AddCommand(cmd)
			root.SetErr(freshnessNoticeFailure{errors.New("notice unavailable")})
			cmd.SetContext(context.Background())
			wrapFreshnessCompletion(root)
			require.NoError(t, cmd.RunE(cmd, nil))
			if logging {
				require.Contains(t, logs.String(), "notice unavailable")
			}
		})
	}
}

func TestJSONWriter_FreshnessSnapshotSurvivesCommandScopeChange(t *testing.T) {
	saveAndResetApp(t)
	app.outputScope, app.staleReason = freshnessRead, "sync_lock_busy"
	var output bytes.Buffer
	reader := newJSONOutputWriter(&output)
	app.outputScope, app.staleReason = freshnessWrite, ""
	require.NoError(t, reader.WriteJSON(map[string]int{"value": 1}))
	require.JSONEq(t, `{"value":1,"stale":true,"stale_reason":"sync_lock_busy"}`, output.String())
	app.staleReason = "sync_lock_busy"
	writer := newJSONOutputWriter(io.Discard)
	require.Empty(t, writer.staleReason)
	require.Equal(t, "sync_lock_busy", app.staleReason, "acknowledgement suppression never clears the busy evidence")
}
