// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

func TestDoctorService_StatusRedaction_TrustedEnumsAndCredentialFields(t *testing.T) {
	for _, status := range []DoctorStatus{DoctorPass, DoctorWarn, DoctorFail, DoctorInfo} {
		t.Run(string(status), func(t *testing.T) {
			secret := string(status)
			svc := NewDoctorService(DoctorOptions{Scrub: func(s string) string { return redact.Known(s, secret) }, CollectSync: func(context.Context) ([]DoctorResult, error) {
				return []DoctorResult{{ID: "sync." + secret, Status: status, Detail: secret, Fix: secret}}, nil
			}})
			rows, err := svc.Run(t.Context(), DoctorRunOptions{})
			if status == DoctorFail {
				assert.ErrorIs(t, err, ErrDoctorChecksFailed)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, rows, 1)
			assert.Equal(t, status, rows[0].Status, "validated outcome is trusted protocol data")
			assert.Equal(t, "sync.REDACTED", rows[0].ID)
			assert.Equal(t, "REDACTED", rows[0].Detail)
			assert.Equal(t, "REDACTED", rows[0].Fix)
		})
	}
}

func TestDoctorService_StatusRedaction_InvalidDiagnosticsAndCauses(t *testing.T) {
	const secret = "synthetic_status_secret"
	svc := NewDoctorService(DoctorOptions{Scrub: func(s string) string { return redact.Known(s, secret) }})
	require.NoError(t, svc.Register(DoctorCheck{ID: "invalid", Run: func(context.Context) DoctorResult {
		return DoctorResult{Status: DoctorStatus(secret), Detail: secret, Fix: secret}
	}}))
	rows, err := svc.Run(t.Context(), DoctorRunOptions{})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.Contains(t, err.Error(), "REDACTED")
	require.Len(t, rows, 1)
	assert.Equal(t, DoctorStatus("REDACTED"), rows[0].Status, "untrusted invalid status still needs scrubbing")
	cause := errors.New("collector " + secret)
	wrapped := svc.RedactError(cause)
	assert.ErrorIs(t, wrapped, cause)
	assert.NotContains(t, wrapped.Error(), secret)
	assert.Contains(t, wrapped.Error(), "REDACTED")
}
