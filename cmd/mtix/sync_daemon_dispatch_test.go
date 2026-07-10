// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-52.2: `mtix sync daemon --dispatch-hooks` designates this host as the
// hook dispatcher. This guards the flag wiring; the dispatch behavior itself is
// covered by the service unit tests (hooks_synced_dispatch_test.go) and the
// cloud e2e (TestE2E_FR19_DesignatedHost_SyncedDispatch...).
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncDaemonCmd_HasDispatchHooksFlag(t *testing.T) {
	cmd := newSyncDaemonCmd()
	f := cmd.Flags().Lookup("dispatch-hooks")
	require.NotNil(t, f, "the daemon must expose --dispatch-hooks for the designated dispatcher (MTIX-52)")
	assert.Equal(t, "false", f.DefValue, "off by default — a host is the dispatcher only when explicitly designated")
}
