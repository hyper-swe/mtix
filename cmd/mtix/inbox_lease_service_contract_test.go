// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunLeaseOperation_InvalidTTLAndNoProject_PreservesTTLPrecedence(t *testing.T) {
	before := app.store
	app.store = nil
	t.Cleanup(func() { app.store = before })
	err := runLeaseOperation(newInboxCmd(), "acquire", leaseOptions{ttl: "59s"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Equal(t, "lease ttl must be between 1m and 24h: invalid input", err.Error())
}

func TestInboxLeaseCmd_GlobalJSON_PreservesLeaseShapes(t *testing.T) {
	setupLeaseApp(t)
	before := app.jsonOutput
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = before })
	text, err := executeLease(t, "acquire", "--agent", "TEST-agent")
	require.NoError(t, err)
	var lease map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &lease))
	assert.Len(t, lease, 5)
	assert.Equal(t, "TEST-agent", lease["agent_id"])
	holder, ok := lease["holder"].(string)
	require.True(t, ok)
	text, err = executeLease(t, "release", "--agent", "TEST-agent", "--holder", holder, "--abandon")
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &result))
	assert.Len(t, result, 3)
	assert.Equal(t, "abandoned", result["outcome"])
	assert.NotContains(t, result, "expires_at")
}
