// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"runtime"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// OperatorState supplies an isolated operator configuration for hook tests.
func OperatorState(t *testing.T) *oplocal.State {
	t.Helper()
	l := hostguard.Isolate(t)
	return oplocal.New(oplocal.Env{GOOS: runtime.GOOS, Home: l.Home, Values: map[string]string{"XDG_CONFIG_HOME": l.Config, "APPDATA": l.Config}})
}
