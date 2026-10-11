// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Protocol output tests keep command diagnostics separate from wake state.

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wakeProtocolWrapper(t *testing.T, f wakeScriptFixture, body string) string {
	t.Helper()
	path := filepath.Join(f.root, "protocol-mtix")
	text := "#!/bin/sh\n" + body + "\nexec \"$WAKE_REAL_MTIX\" \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(text), 0700))
	return path
}

func TestWakeAgentScript_SuccessfulDiagnostics_PreservesProtocol(t *testing.T) {
	for _, phase := range []string{"acquire", "release"} {
		t.Run(phase, func(t *testing.T) {
			f := newWakeScriptFixture(t)
			f.event(t, "Handle the event")
			body := "if [ \"$1 $2 $3\" = \"inbox lease $WAKE_WARNING_PHASE\" ]; then printf '%s\\n' 'protocol diagnostic' >&2; fi"
			wrapper := wakeProtocolWrapper(t, f, body)
			p := f.start(t, "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary, "WAKE_WARNING_PHASE="+phase)
			assert.NoError(t, p.wait(t))
			calls, err := os.ReadFile(filepath.Join(f.root, "calls"))
			assert.NoError(t, err)
			assert.Len(t, strings.Fields(string(calls)), 1)
			log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
			assert.NoError(t, err)
			assert.Contains(t, string(log), "protocol diagnostic")
			pending := f.cli(t, "inbox", "--agent", "TEST-agent", "--json")
			assert.NotContains(t, string(pending), "Handle the event")
		})
	}
}

func TestWakeAgentScript_MalformedHolder_ReportsBeforeLaunch(t *testing.T) {
	tests := []struct{ name, value string }{
		{"Empty", ""}, {"Short", "abc"}, {"Long", strings.Repeat("a", 33)},
		{"Upper", strings.Repeat("A", 32)}, {"NonHex", strings.Repeat("g", 32)},
		{"ExtraLine", strings.Repeat("a", 32) + "\nextra"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newWakeScriptFixture(t)
			f.event(t, "Handle the event")
			body := "if [ \"$1 $2 $3\" = 'inbox lease acquire' ]; then printf '%s\\n' \"$WAKE_PROTOCOL_VALUE\"; exit 0; fi"
			wrapper := wakeProtocolWrapper(t, f, body)
			p := f.start(t, "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary, "WAKE_PROTOCOL_VALUE="+tt.value)
			assert.Error(t, p.wait(t))
			assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
			_, err := os.Stat(filepath.Join(f.root, "calls"))
			assert.ErrorIs(t, err, os.ErrNotExist)
			log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
			assert.NoError(t, err)
			assert.Contains(t, string(log), "invalid lease holder")
		})
	}
}

func TestWakeAgentScript_MalformedOutcome_ReportsFailure(t *testing.T) {
	for _, value := range []string{"", "released extra", "released\nextra", "unknown"} {
		t.Run(strings.ReplaceAll(value, "\n", "Line"), func(t *testing.T) {
			f := newWakeScriptFixture(t)
			body := "if [ \"$1 $2 $3\" = 'inbox lease release' ]; then case \" $* \" in *' --abandon '*) ;; *) printf '%s\\n' \"$WAKE_PROTOCOL_VALUE\"; exit 0 ;; esac; fi"
			wrapper := wakeProtocolWrapper(t, f, body)
			p := f.start(t, "MTIX_BIN="+wrapper, "WAKE_REAL_MTIX="+f.binary, "WAKE_PROTOCOL_VALUE="+value)
			assert.Error(t, p.wait(t))
			assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
			log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
			assert.NoError(t, err)
			assert.Contains(t, string(log), "unexpected lease release outcome")
		})
	}
}
