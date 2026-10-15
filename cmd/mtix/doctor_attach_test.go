// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func attachDoctorRows(t *testing.T, deps attachDoctorDeps, fix bool) map[string]service.DoctorResult {
	t.Helper()
	svc := service.NewDoctorService(service.DoctorOptions{Platform: "linux"})
	require.NoError(t, registerAttachDoctorChecks(context.Background(), svc, deps))
	rows, _ := svc.Run(context.Background(), service.DoctorRunOptions{Fix: fix}) //nolint:errcheck // FAIL rows are asserted per case
	out := map[string]service.DoctorResult{}
	for _, row := range rows {
		out[row.ID] = row
	}
	return out
}

func attachDoctorTestDeps(t *testing.T, st *oplocal.State, current int, writers ...attach.Writer) attachDoctorDeps {
	t.Helper()
	all := append([]attach.Writer{attach.NewShimWriter(st, current, attach.ValidateShimTarget)}, writers...)
	reg, err := attach.NewRegistry(current, all...)
	require.NoError(t, err)
	return attachDoctorDeps{state: st, registry: reg, current: current, clock: func() time.Time { return attachTestNow }}
}

func TestDoctorAttach_NothingInstalled_NoRows(t *testing.T) {
	st := testutil.OperatorState(t)
	rows := attachDoctorRows(t, attachDoctorTestDeps(t, st, attach.Protocol), false)
	assert.Empty(t, rows, "a host without attach artifacts reports nothing")
}

func TestDoctorAttach_SetupShim_StatesAndFixes(t *testing.T) {
	t.Run("current shim passes", func(t *testing.T) {
		st := testutil.OperatorState(t)
		installTestShim(t, st)
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, attach.Protocol), false)["setup.shim"]
		assert.Equal(t, service.DoctorPass, row.Status, row.Detail)
	})
	t.Run("stale shim warns and doctor --fix rewrites it", func(t *testing.T) {
		st := testutil.OperatorState(t)
		installTestShim(t, st)
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), false)["setup.shim"]
		assert.Equal(t, service.DoctorWarn, row.Status)
		assert.Equal(t, "mtix doctor --fix", row.Fix)
		row = attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), true)["setup.shim"]
		assert.Equal(t, service.DoctorPass, row.Status, row.Detail)
	})
	t.Run("refused target fails with the ownership fix", func(t *testing.T) {
		st := testutil.OperatorState(t)
		_, target := installTestShim(t, st)
		require.NoError(t, os.Chmod(target, 0o770))
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, attach.Protocol), true)["setup.shim"]
		assert.Equal(t, service.DoctorFail, row.Status)
		assert.Contains(t, row.Detail, "writable by group or others")
		assert.Contains(t, row.Fix, "chmod go-w")
		assert.Contains(t, row.Fix, target)
	})
	t.Run("shim without its record fails with removal fix", func(t *testing.T) {
		st := testutil.OperatorState(t)
		path, err := st.WriteExecutable(attach.ShimName, []byte("#!/bin/sh\n"))
		require.NoError(t, err)
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, attach.Protocol), false)["setup.shim"]
		assert.Equal(t, service.DoctorFail, row.Status)
		assert.Equal(t, "rm "+path, row.Fix)
	})
}

func TestDoctorAttach_GlueRows_PerMarkedHarness(t *testing.T) {
	t.Run("N-1 glue with a writer is refreshed by doctor --fix", func(t *testing.T) {
		st := testutil.OperatorState(t)
		require.NoError(t, attach.MarkGlueForRefresh(st, "claude-code", 1))
		glue := &testGlueWriter{harness: "claude-code"}
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2, glue), false)["glue.claude-code"]
		assert.Equal(t, service.DoctorWarn, row.Status)
		assert.Equal(t, "mtix doctor --fix", row.Fix)
		row = attachDoctorRows(t, attachDoctorTestDeps(t, st, 2, glue), true)["glue.claude-code"]
		assert.Equal(t, service.DoctorPass, row.Status, row.Detail)
		assert.Equal(t, 1, glue.writes)
	})
	t.Run("marker with no writer in this build", func(t *testing.T) {
		st := testutil.OperatorState(t)
		require.NoError(t, attach.MarkGlueForRefresh(st, "codex", 1))
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), true)["glue.codex"]
		assert.Equal(t, service.DoctorWarn, row.Status)
		assert.Contains(t, row.Detail, "attach protocol 1")
		assert.NotEmpty(t, row.Fix)
		assert.NotContains(t, row.Fix, "mtix setup")
		assert.NotEqual(t, "mtix doctor --fix", row.Fix, "doctor --fix cannot repair it, so it is not offered")
	})
	t.Run("glue newer than this binary", func(t *testing.T) {
		st := testutil.OperatorState(t)
		require.NoError(t, attach.MarkGlueForRefresh(st, "pi", 3))
		row := attachDoctorRows(t, attachDoctorTestDeps(t, st, 1), false)["glue.pi"]
		assert.Equal(t, service.DoctorWarn, row.Status)
		assert.Contains(t, row.Fix, "mtix version")
	})
}

func TestDoctorAttach_Failures_ShownWithCountAndFix(t *testing.T) {
	st := testutil.OperatorState(t)
	_, _, err := runAttachVerbForTest(t, attachTestDeps(st, 2, map[string]attach.Adapter{}), "session-start", "--harness", "pi", "--protocol", "2")
	require.NoError(t, err)
	row := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), false)["attach.failures"]
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "no pi adapter")
	assert.Contains(t, row.Detail, "count 1")
}

func TestDoctorAttach_SkewDispatchers_NoServiceNamed(t *testing.T) {
	st := testutil.OperatorState(t)
	deps := attachDoctorTestDeps(t, st, 2)
	deps.dispatchers = []service.DispatcherStatus{
		{PID: 4242, Protocol: 1, Version: "0.6.0", Older: true},
		{PID: 77, Protocol: 2, Version: "0.6.1", BelowFloor: true, Floor: 3},
	}
	row := attachDoctorRows(t, deps, false)["skew.dispatchers"]
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "PID 4242")
	assert.Contains(t, row.Detail, "no dispatcher service is installed")
	assert.Contains(t, row.Detail, "min_dispatch_protocol 3")
	assert.Contains(t, row.Fix, "4242")

	deps.dispatchers = []service.DispatcherStatus{{PID: 9, Protocol: 2, Version: "0.6.1"}}
	row = attachDoctorRows(t, deps, false)["skew.dispatchers"]
	assert.Equal(t, service.DoctorPass, row.Status)
	assert.Empty(t, row.Fix)
}

func TestDoctorAttach_JSONRows_HaveExactFields(t *testing.T) {
	st := testutil.OperatorState(t)
	require.NoError(t, attach.MarkGlueForRefresh(st, "codex", 1))
	rows := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), false)
	data, err := json.Marshal(rows["glue.codex"])
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(data, &fields))
	assert.ElementsMatch(t, []string{"id", "status", "detail", "fix"}, keys(fields))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDoctor_NoUsableOperatorState_NoErrorAndNoFail(t *testing.T) {
	tests := []struct {
		name    string
		env     func(t *testing.T)
		wantFix string
	}{
		{"HOME unset", func(t *testing.T) {
			t.Setenv("HOME", "")
			t.Setenv("XDG_CONFIG_HOME", "")
		}, "HOME"},
		{"operator directory unusable", func(t *testing.T) {
			testutil.OperatorState(t)
			t.Setenv("XDG_CONFIG_HOME", "relative/path")
		}, "mtix hooks status --json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveAndResetApp(t)
			t.Chdir(t.TempDir())
			tt.env(t)
			svc, err := newConfiguredDoctorService()
			require.NoError(t, err, "doctor never fails to start over operator state")
			rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
			require.NoError(t, err, "nothing installed: no FAIL, exit as on base")
			for _, row := range rows {
				assert.NotEqual(t, service.DoctorFail, row.Status, row.ID)
				if row.ID == "attach.state" {
					assert.Equal(t, service.DoctorInfo, row.Status)
					assert.Contains(t, row.Fix, tt.wantFix)
				}
			}
		})
	}
}

func TestDoctorAttach_Fixes_NeverJustMtixDoctor(t *testing.T) {
	st := testutil.OperatorState(t)
	_, _, err := runAttachVerbForTest(t, attachTestDeps(st, 2, map[string]attach.Adapter{}), "session-start", "--harness", "pi", "--protocol", "2")
	require.NoError(t, err)
	require.NoError(t, attach.MarkGlueForRefresh(st, "codex", 1))
	for id, row := range attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), false) {
		assert.NotEqual(t, "mtix doctor", row.Fix, id)
	}
	for _, alert := range attachAlerts(t, st) {
		assert.NotEqual(t, "mtix doctor", alert.Fix, alert.Class)
	}
}
