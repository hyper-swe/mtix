// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Count output and flags are exercised against existing command constructors.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxCountCLI_TextAndJSON_ReturnsPendingAndTotal(t *testing.T) {
	for _, tt := range []struct {
		name      string
		json, all bool
		want      string
	}{{"pending", false, false, "1\n"}, {"total", false, true, "2\n"}, {"json", true, false, ""}, {"json all", true, true, ""}} {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			events := envelopeCLIEvents(t, "one", "two")
			require.NoError(t, app.store.InboxAck(t.Context(), "worker", events[1].Seq))
			app.jsonOutput = tt.json
			args := []string{"--agent", "worker", "--count"}
			if tt.all {
				args = append(args, "--all")
			}
			out := outputInboxCommand(t, args...)
			if !tt.json {
				require.Equal(t, tt.want, out)
				return
			}
			var actual map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(out), &actual))
			require.Len(t, actual, 3)
			require.JSONEq(t, `{"agent":"worker","pending":1,"total":2}`, out)
			require.Equal(t, "1", string(actual["pending"]))
			require.Equal(t, "2", string(actual["total"]))
		})
	}
}

func TestInboxCountCLI_NeverAddressed_ReturnsIntegerZeros(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	require.JSONEq(t, `{"agent":"never","pending":0,"total":0}`, outputInboxCommand(t, "--agent", "never", "--count"))
	app.jsonOutput = false
	require.Equal(t, "0\n", outputInboxCommand(t, "--agent", "never", "--count", "--all"))
}

func TestInboxCountCLI_InvalidFlags_RejectsBeforeStartup(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		message string
	}{
		{"wait", []string{"--count", "--wait"}, "--count cannot be combined with --wait"},
		{"wait false", []string{"--count", "--wait=false"}, "--count cannot be combined with --wait"},
		{"format", []string{"--count", "--format=prompt"}, "--count cannot be combined with --format"},
		{"empty format", []string{"--count", "--format="}, "--count cannot be combined with --format"},
		{"all", []string{"--all"}, "--all requires --count"},
		{"all false", []string{"--all=false"}, "--all requires --count"},
		{"count false all", []string{"--count=false", "--all"}, "--all requires --count"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			saveAndResetApp(t)
			identityOperatorState(t)
			dir := t.TempDir()
			t.Chdir(dir)
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".mtix"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".mtix", "config.yaml"), []byte("[bad config"), 0600))
			root := newRootCmd()
			root.SetArgs(append([]string{"-C", dir, "inbox", "--agent", "worker"}, tt.args...))
			err := root.Execute()
			require.ErrorContains(t, err, tt.message)
			require.Nil(t, app.store)
			_, err = os.Stat(filepath.Join(dir, ".mtix", "data"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestInboxCountCLI_Subcommands_RejectParentCountFlags(t *testing.T) {
	for _, flag := range []string{"--count", "--all", "--count=false", "--all=false"} {
		t.Run(flag, func(t *testing.T) {
			initTestApp(t)
			events := envelopeCLIEvents(t, "unhandled")
			root := newRootCmd()
			root.SetArgs([]string{"inbox", flag, "ack", "1", "--agent", "worker"})
			require.ErrorContains(t, root.Execute(), "unknown flag:")
			pending, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Equal(t, events, pending)
		})
	}
}

func TestInboxCountCLI_CountFalseAndTimeout_PreserveBehavior(t *testing.T) {
	initTestApp(t)
	require.Equal(t, "(inbox empty)\n", outputInboxCommand(t, "--agent", "never", "--count=false"))
	require.Equal(t, "0\n", outputInboxCommand(t, "--agent", "never", "--count", "--timeout=0"))
}

func TestInboxCountCLI_EnvelopeCaps_DoNotLimitCounts(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "first", "second")
	envelopeWakeConfig(t, map[string]any{"host_id": "foreign", "data": map[string]any{"max_events": -1}})
	require.Equal(t, "2\n", outputInboxCommand(t, "--agent", "worker", "--count"))
	require.Equal(t, "2\n", outputInboxCommand(t, "--agent", "worker", "--count", "--all"))
}
