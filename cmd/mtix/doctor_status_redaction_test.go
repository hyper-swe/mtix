// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

func TestDoctor_StatusRedaction_TrustedEnumsInTextAndJSON(t *testing.T) {
	for _, status := range []service.DoctorStatus{service.DoctorPass, service.DoctorWarn, service.DoctorFail, service.DoctorInfo} {
		for _, flags := range [][]string{nil, {"--json"}} {
			t.Run(string(status)+strings.Join(flags, ""), func(t *testing.T) {
				secret := string(status)
				svc := service.NewDoctorService(service.DoctorOptions{Scrub: func(s string) string { return redact.Known(s, secret) }})
				require.NoError(t, svc.Register(service.DoctorCheck{ID: "check." + secret, Run: func(context.Context) service.DoctorResult {
					return service.DoctorResult{Status: status, Detail: secret, Fix: secret}
				}}))
				text, err := doctorRegisteredOutput(t, svc, flags...)
				wantExit := 0
				if status == service.DoctorFail {
					wantExit = 2
					assert.ErrorIs(t, err, service.ErrDoctorChecksFailed)
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, wantExit, exitCodeForError(err))
				if len(flags) > 0 {
					rows := doctorCLIRows(t, text)
					require.Len(t, rows, 1)
					assert.Equal(t, map[string]string{"id": "check.REDACTED", "status": string(status), "detail": "REDACTED", "fix": "REDACTED"}, rows[0])
				} else {
					assert.Contains(t, text, "["+strings.ToUpper(string(status))+"] check.REDACTED")
					assert.NotContains(t, text, "check."+secret)
					assert.NotContains(t, text, "  "+secret)
					assert.Contains(t, text, "REDACTED")
				}
			})
		}
	}
}

func TestDoctorCLI_StatusRedaction_MalformedDSNAndURLPasswordControl(t *testing.T) {
	for _, dsn := range []string{"pass", "warn", "fail", "info", "postgres://fixture:fail@/%zz"} {
		t.Run(dsn, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			for _, flags := range [][]string{nil, {"--json"}} {
				out := doctorCLIExecute(t, root, dsn, append([]string{"doctor"}, flags...)...)
				assert.Equal(t, 2, out.exit, out.stderr)
				assert.Empty(t, out.stderr)
				if len(flags) > 0 {
					rows := doctorCLIRows(t, out.stdout)
					require.NotEmpty(t, rows)
					assert.Equal(t, "sync.pg-reachable", rows[0]["id"])
					assert.Equal(t, "fail", rows[0]["status"])
					for _, row := range rows {
						assert.Contains(t, []string{"pass", "warn", "fail", "info"}, row["status"])
						assert.NotContains(t, row["detail"]+row["fix"], dsn)
					}
				} else {
					assert.Contains(t, out.stdout, "[FAIL] sync.pg-reachable")
					assert.NotContains(t, out.stdout, "[REDACTED]")
				}
				if strings.HasPrefix(dsn, "postgres://") {
					assert.NotContains(t, out.stdout+out.stderr, dsn)
				}
			}
		})
	}
}
