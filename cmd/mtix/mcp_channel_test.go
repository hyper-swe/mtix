// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56.7: --channel-agent flag wiring; the protocol behavior is pinned by
// internal/mcp/channel_contract_test.go and the pump by internal/channel.
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPCmd_ChannelAgentFlag(t *testing.T) {
	cmd := newMCPCmd()
	f := cmd.Flags().Lookup("channel-agent")
	require.NotNil(t, f, "mcp --channel-agent declared (MTIX-56.7)")
	assert.Equal(t, "", f.DefValue, "channel mode is opt-in; plain mcp serves tools only")
}
