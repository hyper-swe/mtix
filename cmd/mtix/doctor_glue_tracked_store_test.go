// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestDoctorGlueTrackedStore_Reasons_RowsAndFixes(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   bool
		fix    string
	}{
		{"tracked store", attach.ReasonTrackedStore, true, trackedStoreFix},
		{"unreadable index", attach.ReasonIndexUnsupported, true, unreadableIndexFix},
		{"unlisted is not a problem", attach.ReasonUnlisted, false, ""},
		{"attached is not a problem", attach.ReasonAttached, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			deps := attachDoctorTestDeps(t, st, 2)
			deps.glue = func(context.Context) attach.GlueDecision { return attach.GlueDecision{Reason: tt.reason} }
			row, ok := attachDoctorRows(t, deps, false)["glue.tracked-store"]
			require.Equal(t, tt.want, ok)
			if ok {
				assert.Equal(t, service.DoctorWarn, row.Status)
				assert.Equal(t, tt.fix, row.Fix, "the exact fix, never just mtix doctor")
				assert.NotEmpty(t, row.Detail)
			}
		})
	}
}

func TestDoctorGlueTrackedStore_NoPredicate_NoRow(t *testing.T) {
	st := testutil.OperatorState(t)
	_, ok := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), false)["glue.tracked-store"]
	assert.False(t, ok)
}
