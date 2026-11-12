// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pins that every route builds its wake ledger key through model.NewWakeKey, so two armed
// routes can never disagree on the key of one event and wake twice (MTIX-112.40.13).
package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWakeKey_RouteSources_BuildKeysOnlyThroughTheConstructor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		checked++
		require.False(t, strings.Contains(string(b), "model.WakeKey{AgentID"), "%s builds a wake key by hand; use model.NewWakeKey", f)
	}
	require.Greater(t, checked, 20, "the scan must see the service sources")
}
