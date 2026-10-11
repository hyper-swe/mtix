// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// These tests verify the inbox lease command contract and error classification.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestInboxLeaseCmd_SubcommandsRegistered_ExposeLeaseHelp(t *testing.T) {
	for _, verb := range []string{"acquire", "release"} {
		t.Run(verb, func(t *testing.T) {
			cmd, remaining, err := newInboxCmd().Find([]string{"lease", verb})
			require.NoError(t, err)
			assert.Empty(t, remaining)
			assert.Equal(t, verb, cmd.Name())
			assert.NotNil(t, cmd.Flags().Lookup("ttl"))
			assert.Nil(t, cmd.Flags().Lookup("host"))
			assert.Nil(t, cmd.Flags().Lookup("uid"))
		})
	}
}

func setupLeaseApp(t *testing.T) {
	t.Helper()
	initTestApp(t)
	root := operatorTestHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "separate"))
}

func executeLease(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var result error
	text := captureStdout(t, func() { _, result = executeCmd(newInboxCmd(), append([]string{"lease"}, args...)...) })
	return text, result
}

func TestInboxLeaseCmd_HeldByOther_Exit6(t *testing.T) {
	setupLeaseApp(t)
	first, err := executeLease(t, "acquire", "--agent", "TEST-agent")
	require.NoError(t, err)
	assert.Regexp(t, "^[0-9a-f]{32}\n$", first)
	text, err := executeLease(t, "acquire", "--agent", "TEST-agent")
	require.Error(t, err)
	assert.Empty(t, text)
	assert.Equal(t, 6, exitCodeForError(err))
	assert.ErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Contains(t, err.Error(), "lease for TEST-agent held by another run until ")
	assert.Contains(t, err.Error(), "; marked dirty, the holder re-checks before release")
}

func TestInboxLeaseCmd_TTLOutOfRange_ReturnsInvalidInput(t *testing.T) {
	for _, ttl := range []string{"0", "-1m", "59s", "24h1ns", "999999999999999999999h", "wrong"} {
		t.Run(ttl, func(t *testing.T) {
			setupLeaseApp(t)
			_, err := executeLease(t, "acquire", "--agent", "TEST-agent", "--ttl", ttl)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Equal(t, 1, exitCodeForError(err))
		})
	}
}

func TestInboxLeaseCmd_ValidTTLAndJSON_RoundTripsOwnership(t *testing.T) {
	for _, ttl := range []string{"1m", "24h"} {
		t.Run(ttl, func(t *testing.T) {
			setupLeaseApp(t)
			text, err := executeLease(t, "acquire", "--agent", "TEST-agent", "--ttl", ttl, "--json")
			require.NoError(t, err)
			var lease map[string]any
			require.NoError(t, json.Unmarshal([]byte(text), &lease))
			assert.Len(t, lease, 5)
			assert.Equal(t, "TEST-agent", lease["agent_id"])
			holder := lease["holder"].(string)
			text, err = executeLease(t, "acquire", "--agent", "TEST-agent", "--holder", holder)
			require.NoError(t, err)
			assert.Equal(t, holder+"\n", text)
			text, err = executeLease(t, "release", "--agent", "TEST-agent", "--holder", holder, "--json")
			require.NoError(t, err)
			var release map[string]any
			require.NoError(t, json.Unmarshal([]byte(text), &release))
			assert.Equal(t, "released", release["outcome"])
			assert.Len(t, release, 3)
			assert.NotContains(t, release, "expires_at")
			_, err = executeLease(t, "release", "--agent", "TEST-agent", "--holder", holder)
			assert.Equal(t, 6, exitCodeForError(err))
			assert.Equal(t, "not the holder of this lease; it expired and was taken over or released", err.Error())
		})
	}
}

func TestInboxLeaseCmd_Abandon_PrintsOutcomeOnly(t *testing.T) {
	setupLeaseApp(t)
	text, err := executeLease(t, "acquire", "--agent", "TEST-agent")
	require.NoError(t, err)
	text, err = executeLease(t, "release", "--agent", "TEST-agent", "--holder", strings.TrimSpace(text), "--abandon")
	require.NoError(t, err)
	assert.Equal(t, "abandoned\n", text)
}

func TestInboxLeaseCmd_InvalidHolder_ReturnsInvalidInput(t *testing.T) {
	for _, verb := range []string{"acquire", "release"} {
		t.Run(verb, func(t *testing.T) {
			setupLeaseApp(t)
			_, err := executeLease(t, verb, "--agent", "TEST-agent", "--holder", "wrong")
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestExitCodeForError_DiskFullBeatsLeaseHeld(t *testing.T) {
	assert.Equal(t, 3, exitCodeForError(errors.Join(errLeaseHeld, model.ErrDiskFull)))
	assert.Equal(t, 4, exitCodeForError(errors.Join(errLeaseHeld, model.ErrCorrupted)))
	assert.Equal(t, 6, exitCodeForError(errLeaseHeld))
}
