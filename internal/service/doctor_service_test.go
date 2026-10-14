// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor service tests pin registry execution, typed repairs and final redaction.
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

func doctorConstantCheck(id string, status DoctorStatus) DoctorCheck {
	return DoctorCheck{ID: id, Run: func(context.Context) DoctorResult {
		return DoctorResult{Status: status, Detail: "diagnostic", Fix: "printed remedy"}
	}}
}

func TestDoctorService_StatusMatrixAndOrderedRegistration(t *testing.T) {
	for _, status := range []DoctorStatus{DoctorPass, DoctorWarn, DoctorFail, DoctorInfo} {
		t.Run(string(status), func(t *testing.T) {
			svc := NewDoctorService(DoctorOptions{})
			require.NoError(t, svc.Register(doctorConstantCheck("first", status)))
			require.NoError(t, svc.Register(doctorConstantCheck("second", DoctorPass)))
			rows, err := svc.Run(t.Context(), DoctorRunOptions{})
			if status == DoctorFail {
				assert.ErrorIs(t, err, ErrDoctorChecksFailed)
			} else {
				assert.NoError(t, err)
			}
			require.Len(t, rows, 2)
			assert.Equal(t, "first", rows[0].ID)
			assert.Equal(t, status, rows[0].Status)
			assert.Equal(t, "second", rows[1].ID)
		})
	}
}

func TestDoctorService_EmptyAndWindowsInfo(t *testing.T) {
	for _, platform := range []string{"", "linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			rows, err := NewDoctorService(DoctorOptions{Platform: platform}).Run(t.Context(), DoctorRunOptions{Fix: true})
			require.NoError(t, err)
			require.NotNil(t, rows)
			if platform != "windows" {
				assert.Empty(t, rows)
				return
			}
			require.Len(t, rows, 1)
			assert.Equal(t, "platform.windows", rows[0].ID)
			assert.Equal(t, DoctorInfo, rows[0].Status)
			assert.Contains(t, rows[0].Detail, "Built-in wakes unavailable")
			assert.Empty(t, rows[0].Fix)
		})
	}
}

func TestDoctorService_Register_InvalidAndDuplicateRefused(t *testing.T) {
	svc := NewDoctorService(DoctorOptions{})
	for _, check := range []DoctorCheck{{}, {ID: "without-run"}, doctorConstantCheck(" ", DoctorPass)} {
		assert.ErrorIs(t, svc.Register(check), model.ErrInvalidInput)
	}
	require.NoError(t, svc.Register(doctorConstantCheck("once", DoctorPass)))
	assert.ErrorIs(t, svc.Register(doctorConstantCheck("once", DoctorPass)), model.ErrAlreadyExists)
	rows, err := svc.Run(t.Context(), DoctorRunOptions{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestDoctorService_Fix_GrantZeroUnknownAndNilRefused(t *testing.T) {
	for _, kind := range []DoctorRepairKind{DoctorRepairNone, DoctorRepairGrant, DoctorRepairKind(99), DoctorRepairNonGrant} {
		t.Run(string(rune('a'+kind)), func(t *testing.T) {
			svc := NewDoctorService(DoctorOptions{})
			check := doctorConstantCheck("grant", DoctorFail)
			calls := 0
			check.Fixer = &DoctorFixer{Kind: kind, Apply: func(context.Context) error { calls++; return nil }}
			if kind == DoctorRepairNonGrant {
				check.Fixer.Apply = nil
			}
			require.NoError(t, svc.Register(check))
			rows, err := svc.Run(t.Context(), DoctorRunOptions{Fix: true})
			require.ErrorIs(t, err, ErrDoctorChecksFailed)
			require.Len(t, rows, 1)
			assert.Equal(t, 0, calls)
			assert.Equal(t, "printed remedy", rows[0].Fix)
			assert.Contains(t, rows[0].Detail, "automatic repair refused")
		})
	}
}

func TestDoctorService_Fix_PermittedRecheckedAndIdempotent(t *testing.T) {
	svc := NewDoctorService(DoctorOptions{})
	checks, repairs := 0, 0
	check := DoctorCheck{ID: "repair", Run: func(context.Context) DoctorResult {
		checks++
		status := DoctorFail
		if repairs != 0 {
			status = DoctorPass
		}
		return DoctorResult{Status: status}
	}, Fixer: &DoctorFixer{Kind: DoctorRepairNonGrant, Apply: func(context.Context) error { repairs++; return nil }}}
	require.NoError(t, svc.Register(check))
	rows, err := svc.Run(t.Context(), DoctorRunOptions{Fix: true})
	require.NoError(t, err)
	assert.Equal(t, DoctorPass, rows[0].Status)
	assert.Equal(t, 2, checks)
	assert.Equal(t, 1, repairs)
	_, err = svc.Run(t.Context(), DoctorRunOptions{Fix: true})
	require.NoError(t, err)
	assert.Equal(t, 3, checks)
	assert.Equal(t, 1, repairs)
}

func TestDoctorService_FinalFieldsCollectorAndRepairErrorRedacted(t *testing.T) {
	const secret = "postgres://fixture:doctor_service_secret@/%zz"
	scrub := func(s string) string { return redact.Known(s, secret) }
	collects := 0
	svc := NewDoctorService(DoctorOptions{Scrub: scrub, CollectSync: func(context.Context) ([]DoctorResult, error) {
		collects++
		return []DoctorResult{{ID: "sync." + secret, Status: DoctorWarn, Detail: secret, Fix: secret}}, nil
	}})
	check := doctorConstantCheck("repair", DoctorFail)
	check.Fixer = &DoctorFixer{Kind: DoctorRepairNonGrant, Apply: func(context.Context) error { return errors.New(secret) }}
	require.NoError(t, svc.Register(check))
	rows, err := svc.Run(t.Context(), DoctorRunOptions{Fix: true})
	require.ErrorIs(t, err, ErrDoctorChecksFailed)
	require.Len(t, rows, 2)
	assert.Equal(t, 1, collects)
	for _, row := range rows {
		text := row.ID + string(row.Status) + row.Detail + row.Fix
		assert.NotContains(t, text, secret)
		assert.NotContains(t, text, "doctor_service_secret")
		assert.Contains(t, text, "REDACTED")
	}
	assert.Contains(t, rows[1].Detail, "repair failed")
}

func TestDoctorService_CollectorFailurePreservesCauseAndRedacts(t *testing.T) {
	cause := errors.New("postgres://fixture:collector_secret@localhost/db")
	svc := NewDoctorService(DoctorOptions{CollectSync: func(context.Context) ([]DoctorResult, error) { return nil, cause }})
	rows, err := svc.Run(t.Context(), DoctorRunOptions{})
	assert.ErrorIs(t, err, cause)
	assert.NotContains(t, err.Error(), "collector_secret")
	assert.NotNil(t, rows)
}

func TestDoctorService_CanceledContext_NoCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	svc := NewDoctorService(DoctorOptions{CollectSync: func(context.Context) ([]DoctorResult, error) { calls++; return nil, nil }})
	rows, err := svc.Run(ctx, DoctorRunOptions{Fix: true})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, rows)
	assert.Equal(t, 0, calls)
}

func TestDoctorService_InvalidResult_StatusAndCollectorIDRefused(t *testing.T) {
	for _, row := range []DoctorResult{{ID: "bad", Status: "invalid"}, {Status: DoctorPass}} {
		svc := NewDoctorService(DoctorOptions{CollectSync: func(context.Context) ([]DoctorResult, error) { return []DoctorResult{row}, nil }})
		_, err := svc.Run(t.Context(), DoctorRunOptions{})
		assert.ErrorIs(t, err, model.ErrInvalidInput)
	}
	svc := NewDoctorService(DoctorOptions{})
	require.NoError(t, svc.Register(doctorConstantCheck("invalid", "invalid")))
	_, err := svc.Run(t.Context(), DoctorRunOptions{})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestDoctorService_DefaultScrubber_MasksURL(t *testing.T) {
	svc := NewDoctorService(DoctorOptions{})
	check := doctorConstantCheck("local", DoctorPass)
	check.Run = func(context.Context) DoctorResult {
		return DoctorResult{Status: DoctorPass, Detail: "postgres://fixture:default_secret@localhost/db"}
	}
	require.NoError(t, svc.Register(check))
	rows, err := svc.Run(t.Context(), DoctorRunOptions{})
	require.NoError(t, err)
	assert.False(t, strings.Contains(rows[0].Detail, "default_secret"))
}
