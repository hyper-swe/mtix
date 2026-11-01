// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClientHub records the registrations a pull asks for and can fail them.
type fakeClientHub struct {
	calls    []clientIdentityCall
	err      error
	deadline time.Time
}

type clientIdentityCall struct{ prefix, machineHash, cliVersion string }

func (f *fakeClientHub) UpsertProjectClient(ctx context.Context, prefix, machineHash, cliVersion string) error {
	f.calls = append(f.calls, clientIdentityCall{prefix, machineHash, cliVersion})
	f.deadline, _ = ctx.Deadline()
	return f.err
}

// TestRegisterPullClient_Cases: the registration carries the board's sync
// project prefix, the machine hash and the CLI version; a board without a
// prefix, or a client without a machine hash or version, is skipped without
// a call or output; a failure prints one warning that names the grant.
func TestRegisterPullClient_Cases(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		id        clientIdentity
		hubErr    error
		wantCalls []clientIdentityCall
		wantWarn  bool
	}{
		{"registers with prefix hash and version", "TEST", clientIdentity{"hash1", "0.6.0"},
			nil, []clientIdentityCall{{"TEST", "hash1", "0.6.0"}}, false},
		{"empty sync project prefix is skipped", "", clientIdentity{"hash1", "0.6.0"}, nil, nil, false},
		{"no machine hash is skipped", "TEST", clientIdentity{"", "0.6.0"}, nil, nil, false},
		{"no version is skipped", "TEST", clientIdentity{"hash1", ""}, nil, nil, false},
		{"hub failure warns", "TEST", clientIdentity{"hash1", "0.6.0"},
			errors.New("permission denied for table sync_project_clients"),
			[]clientIdentityCall{{"TEST", "hash1", "0.6.0"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			setPeerMeta(t, "meta.sync.project_prefix", tt.prefix)
			hub := &fakeClientHub{err: tt.hubErr}
			var stderr bytes.Buffer

			registerPullClient(context.Background(), &stderr, hub, app.store, tt.id)

			assert.Equal(t, tt.wantCalls, hub.calls)
			if tt.wantWarn {
				assert.Contains(t, stderr.String(), "WARN: pull client registration skipped")
				assert.Contains(t, stderr.String(), "INSERT and UPDATE on sync_project_clients")
				assert.Contains(t, stderr.String(), "permission denied")
				assert.Equal(t, 1, bytes.Count(stderr.Bytes(), []byte("\n")), "one warning line")
			} else {
				assert.Empty(t, stderr.String())
			}
		})
	}
}

// TestRegisterPullClient_NoPrefixRow_IsSkipped: a store with no
// meta.sync.project_prefix row at all registers nothing and says nothing.
func TestRegisterPullClient_NoPrefixRow_IsSkipped(t *testing.T) {
	initTestApp(t)
	execPeer(t, `DELETE FROM meta WHERE key = 'meta.sync.project_prefix'`)
	hub := &fakeClientHub{}
	var stderr bytes.Buffer

	registerPullClient(context.Background(), &stderr, hub, app.store, clientIdentity{"hash1", "0.6.0"})

	require.Empty(t, hub.calls)
	require.Empty(t, stderr.String())
}

// TestRegisterPullClient_WriteIsBoundedByTimeout: the registration write runs
// under a deadline no later than pullClientRegisterTimeout from now, so a
// slow hub cannot hold the pull.
func TestRegisterPullClient_WriteIsBoundedByTimeout(t *testing.T) {
	initTestApp(t)
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	hub := &fakeClientHub{}

	before := time.Now()
	registerPullClient(context.Background(), &bytes.Buffer{}, hub, app.store, clientIdentity{"hash1", "0.6.0"})

	require.False(t, hub.deadline.IsZero(), "the write carries a deadline")
	require.LessOrEqual(t, hub.deadline.Sub(before), pullClientRegisterTimeout+time.Second)
}
