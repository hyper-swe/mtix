// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor lifecycle tests cover cancellation boundaries and repair completion.
package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestDoctorService_CancelDuringCallbacks_StopsAndPreservesCause(t *testing.T) {
	for _, stage := range []string{"collector", "check", "repair", "recheck"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checks := 0
			svc := NewDoctorService(DoctorOptions{CollectSync: func(received context.Context) ([]DoctorResult, error) {
				assert.Same(t, ctx, received)
				if stage == "collector" {
					cancel()
				}
				return nil, nil
			}})
			require.NoError(t, svc.Register(DoctorCheck{ID: "cancel", Run: func(received context.Context) DoctorResult {
				assert.Same(t, ctx, received)
				checks++
				if stage == "check" || (stage == "recheck" && checks == 2) {
					cancel()
				}
				return DoctorResult{Status: DoctorFail}
			}, Fixer: &DoctorFixer{Kind: DoctorRepairNonGrant, Apply: func(context.Context) error {
				if stage == "repair" {
					cancel()
				}
				return nil
			}}}))
			_, err := svc.Run(ctx, DoctorRunOptions{Fix: true})
			assert.ErrorIs(t, err, context.Canceled)
			if stage == "collector" {
				assert.Zero(t, checks)
			}
		})
	}
}

func TestDoctorService_WarnRepair_RecheckedOnlyWithFix(t *testing.T) {
	svc := NewDoctorService(DoctorOptions{})
	repairs := 0
	require.NoError(t, svc.Register(DoctorCheck{ID: "warn", Run: func(context.Context) DoctorResult {
		status := DoctorWarn
		if repairs > 0 {
			status = DoctorPass
		}
		return DoctorResult{Status: status}
	}, Fixer: &DoctorFixer{Kind: DoctorRepairNonGrant, Apply: func(context.Context) error { repairs++; return nil }}}))
	rows, err := svc.Run(t.Context(), DoctorRunOptions{})
	require.NoError(t, err)
	assert.Zero(t, repairs)
	assert.Equal(t, DoctorWarn, rows[0].Status)
	rows, err = svc.Run(t.Context(), DoctorRunOptions{Fix: true})
	require.NoError(t, err)
	assert.Equal(t, 1, repairs)
	assert.Equal(t, DoctorPass, rows[0].Status)
}

func TestDoctorService_RepairSuccessStillFail_UsesRecheckVerdict(t *testing.T) {
	svc := NewDoctorService(DoctorOptions{})
	check := doctorConstantCheck("still-failing", DoctorFail)
	check.Fixer = &DoctorFixer{Kind: DoctorRepairNonGrant, Apply: func(context.Context) error { return nil }}
	require.NoError(t, svc.Register(check))
	rows, err := svc.Run(t.Context(), DoctorRunOptions{Fix: true})
	assert.ErrorIs(t, err, ErrDoctorChecksFailed)
	assert.Equal(t, DoctorFail, rows[0].Status)
}

func TestDoctorService_CollectedDuplicateID_IsAnExecutionError(t *testing.T) {
	svc := NewDoctorService(DoctorOptions{CollectSync: func(context.Context) ([]DoctorResult, error) {
		return []DoctorResult{{ID: "sync.same", Status: DoctorPass}, {ID: "sync.same", Status: DoctorPass}}, nil
	}})
	rows, err := svc.Run(t.Context(), DoctorRunOptions{})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Len(t, rows, 2, "collection must not silently drop a row")
}
