// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestWakeConfig_MCPRefusesMutationAndUnscopedAuthority(t *testing.T) {
	testutil.OperatorState(t)
	cs, err := service.NewConfigService("")
	require.NoError(t, err)
	reg := NewToolRegistry()
	RegisterAllTools(reg, ToolSet{Config: cs})
	// RegisterAllTools is the actual server registry; no config-write tool exists.
	for _, name := range []string{"mtix_config_set", "config_set", "mtix_config_delete"} {
		result, callErr := reg.Call(t.Context(), name, json.RawMessage(`{"key":"wake.live","value":"on"}`))
		require.ErrorContains(t, callErr, "unknown tool")
		require.Nil(t, result)
	}
	// A future adapter using the shared service cannot inherit server TTY authority.
	_, err = cs.Set("wake.live", "on")
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, err = cs.Set("wake.live", "off")
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
