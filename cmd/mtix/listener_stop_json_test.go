// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// requireOneJSONDocument fails unless stdout holds exactly one JSON value and
// nothing else (no prose before or after it).
func requireOneJSONDocument(t *testing.T, stdout string) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewBufferString(stdout))
	var v map[string]any
	require.NoError(t, dec.Decode(&v), "stdout must start with one JSON document, got %q", stdout)
	_, err := dec.Token()
	require.ErrorIs(t, err, io.EOF, "stdout must hold nothing after the JSON document, got %q", stdout)
}

// Under --json an escalated stop keeps stdout to one JSON document: the
// SIGTERM, could-not-be-delivered and failure lines go to the error stream
// (MTIX-276.3.1). Plain text keeps them on stdout.
func TestListenerStop_JSON_EscalatedStopKeepsStdoutOneDocument(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(f *ctlFake)
		wantErr    error
		wantReport bool
		wantStderr string
	}{
		{"sigterm ends the listener", func(f *ctlFake) {
			f.onSignal = func(f *ctlFake) { f.held = false }
		}, nil, true, "sending SIGTERM"},
		{"signal fails and the lock is free", func(f *ctlFake) {
			f.signalErr = errors.New("os: process already finished")
			f.onSignal = func(f *ctlFake) { f.held = false }
		}, nil, true, "could not be delivered"},
		{"signal fails and the lock is held", func(f *ctlFake) {
			f.signalErr = errors.New("operation not permitted")
		}, errListenerStillRunning, false, "could not be delivered"},
		{"still held after SIGTERM", func(*ctlFake) {}, errListenerStillRunning, false, "still running after SIGTERM"},
		{"windows never signals", func(f *ctlFake) { f.goos = "windows" }, errListenerStillRunning, false, "nothing was signaled"},
		{"process changed while waiting", func(f *ctlFake) {
			f.onSleep = func(f *ctlFake) { f.proc = listenerProc("reused-pid") }
		}, errListenerStillRunning, false, "changed while waiting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCtlFake()
			tt.setup(f)
			var stdout, stderr bytes.Buffer
			err := f.ctl().stop(&stdout, listenerStopOpts{timeout: 3 * time.Second, errOut: &stderr}, true)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			if tt.wantReport {
				requireOneJSONDocument(t, stdout.String())
			} else {
				assert.Empty(t, stdout.String(), "a failed stop prints no prose on stdout")
			}
			assert.Contains(t, stderr.String(), tt.wantStderr)
		})
	}
}

// A live waiter that restarts the listener fails the stop; under --json its
// lines go to the error stream and stdout stays empty.
func TestListenerStop_JSON_RespawnLinesGoToStderr(t *testing.T) {
	f := newCtlFake()
	f.waiter()
	f.onSleep = func(f *ctlFake) {
		if f.sleeps == 1 {
			f.held = false
		}
		if f.sleeps == 2 {
			f.held, f.pid, f.proc = true, 77, listenerProc("t2")
		}
	}
	var stdout, stderr bytes.Buffer
	err := f.ctl().stop(&stdout, listenerStopOpts{timeout: 30 * time.Second, errOut: &stderr}, true)
	require.ErrorIs(t, err, errListenerRespawned)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "agent-a")
	assert.Contains(t, stderr.String(), "a live waiter started a new listener")
}

// With no error stream the lines are dropped, never written to stdout.
func TestListenerStop_JSON_NilErrOutDropsProgressLines(t *testing.T) {
	f := newCtlFake()
	f.onSignal = func(f *ctlFake) { f.held = false }
	var stdout bytes.Buffer
	require.NoError(t, f.ctl().stop(&stdout, listenerStopOpts{timeout: 3 * time.Second}, true))
	requireOneJSONDocument(t, stdout.String())
}

// Plain text keeps the progress lines on stdout.
func TestListenerStop_Text_EscalationLinesStayOnStdout(t *testing.T) {
	f := newCtlFake()
	f.proc = procinfo.Proc{Args: []string{"/usr/bin/mtix", "daemon", "--listen"}, StartTime: "t1"}
	f.onSignal = func(f *ctlFake) { f.held = false }
	var stdout, stderr bytes.Buffer
	require.NoError(t, f.ctl().stop(&stdout, listenerStopOpts{timeout: 3 * time.Second, errOut: &stderr}, false))
	assert.Contains(t, stdout.String(), "sending SIGTERM")
	assert.Empty(t, stderr.String())
}

// runListenerVerbStreams runs a listener verb through its real RunE wiring and
// returns stdout and stderr separately.
func runListenerVerbStreams(t *testing.T, c *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	require.NoError(t, c.ParseFlags(args))
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetContext(t.Context())
	err = c.RunE(c, nil)
	return out.String(), errb.String(), err
}

// The command wiring hands stop the command's own stderr: an escalated
// `daemon stop --listener --json` prints one JSON document on stdout and the
// SIGTERM line on stderr (MTIX-276.3.1). Passing stdout as the error stream,
// or dropping it, is caught here.
func TestDaemonStop_ListenerJSON_CommandPathSplitsStdoutAndStderr(t *testing.T) {
	f := newCtlFake()
	f.onSignal = func(f *ctlFake) { f.held = false }
	verbs := listenerVerbs(t, f, &fakeManager{})
	stdout, stderr, err := runListenerVerbStreams(t, verbs["stop"], "--listener", "--json", "--timeout", "3s")
	require.NoError(t, err)
	requireOneJSONDocument(t, stdout)
	assert.Contains(t, stderr, "sending SIGTERM")
	assert.NotContains(t, stdout, "SIGTERM")
}

// The same split holds for the failure path of the command: no prose on
// stdout, the could-not-be-delivered line on stderr.
func TestDaemonStop_ListenerJSON_CommandPathFailureKeepsStdoutEmpty(t *testing.T) {
	f := newCtlFake()
	f.signalErr = errors.New("operation not permitted")
	verbs := listenerVerbs(t, f, &fakeManager{})
	stdout, stderr, err := runListenerVerbStreams(t, verbs["stop"], "--listener", "--json", "--timeout", "3s")
	require.ErrorIs(t, err, errListenerStillRunning)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "could not be delivered")
}

// A live waiter that does not restart the listener leaves the stop green. The
// waiters-remain hint is plain text only: under --json stdout is the report
// alone, with the waiter inside it (MTIX-276.3.1).
func TestListenerStop_LiveWaiterRemains_HintIsTextOnly(t *testing.T) {
	setup := func() *ctlFake {
		f := newCtlFake()
		f.waiter()
		f.onSleep = func(f *ctlFake) { f.held = false }
		return f
	}
	var jsonOut bytes.Buffer
	require.NoError(t, setup().ctl().stop(&jsonOut, listenerStopOpts{timeout: 30 * time.Second, errOut: io.Discard}, true))
	requireOneJSONDocument(t, jsonOut.String())
	assert.Contains(t, jsonOut.String(), "agent-a")
	assert.NotContains(t, jsonOut.String(), "live waiters remain")

	var textOut bytes.Buffer
	require.NoError(t, setup().ctl().stop(&textOut, listenerStopOpts{timeout: 30 * time.Second}, false))
	assert.Contains(t, textOut.String(), "live waiters remain")

	verbs := listenerVerbs(t, setup(), &fakeManager{})
	stdout, stderr, err := runListenerVerbStreams(t, verbs["stop"], "--listener", "--json")
	require.NoError(t, err)
	requireOneJSONDocument(t, stdout)
	assert.NotContains(t, stdout+stderr, "live waiters remain")
}
