// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// MTIX-112.19.7 (trunk rule 1): the production registry has no pi glue writer, so
// refresh and doctor --fix can never install the extension by default.
func TestProductionAttachRegistry_NoPiGlueByDefault(t *testing.T) {
	l := hostguard.Isolate(t)
	st := oplocal.New(oplocal.Env{GOOS: runtime.GOOS, Home: l.Home, Values: map[string]string{"XDG_CONFIG_HOME": l.Config, "APPDATA": l.Config}})
	reg, err := productionAttachRegistry(st, attach.Protocol)
	require.NoError(t, err)
	assert.Nil(t, reg.Writer("glue.pi"))
	assert.False(t, productionGlueWriter("pi"))

	_, err = reg.Refresh(context.Background(), st)
	require.NoError(t, err)
	var found []string
	require.NoError(t, filepath.Walk(l.Home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(strings.ToLower(p), "pi") && strings.HasSuffix(p, ".ts") {
			found = append(found, p)
		}
		return err
	}))
	assert.Empty(t, found, "refresh wrote a pi extension")
}

func TestProductionAttachDeps_PiAdapterRegistered(t *testing.T) {
	deps := productionAttachDeps()
	assert.Contains(t, deps.adapters, attach.HarnessPi)
	assert.NotContains(t, deps.adapters, attach.HarnessCodex, "other harness adapters stay out of this change")
}

func TestAttachVerb_SessionFlag_ReachesTheAdapter(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 1, map[string]attach.Adapter{"pi": adapter})
	_, _, err := runAttachVerbForTest(t, deps, "wait", "--harness", "pi", "--protocol", "1", "--session", "abc-1")
	require.NoError(t, err)
	require.Len(t, adapter.calls, 1)
	assert.Equal(t, "abc-1", adapter.calls[0].Session)
}
