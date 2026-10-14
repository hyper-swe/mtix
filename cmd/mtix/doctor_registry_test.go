// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor command tests wire independent service registries into the real command adapter.
package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func doctorRegisteredOutput(t *testing.T, svc *service.DoctorService, flags ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "mtix", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().Bool("json", false, "JSON output")
	root.AddCommand(newDoctorCommand(func() (*service.DoctorService, error) { return svc, nil }))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"doctor"}, flags...))
	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

func TestDoctor_OneLineRegistration_TextJSONAndStatusExitMatrix(t *testing.T) {
	for _, status := range []service.DoctorStatus{service.DoctorPass, service.DoctorWarn, service.DoctorFail, service.DoctorInfo} {
		for _, jsonFlag := range []bool{false, true} {
			t.Run(string(status)+strings.Repeat("-json", boolToInt(jsonFlag)), func(t *testing.T) {
				svc := service.NewDoctorService(service.DoctorOptions{})
				require.NoError(t, svc.Register(service.DoctorCheck{ID: "extra.check", Run: func(context.Context) service.DoctorResult {
					return service.DoctorResult{Status: status, Detail: "one diagnostic", Fix: "one remedy"}
				}}))
				var flags []string
				if jsonFlag {
					flags = []string{"--json"}
				}
				text, err := doctorRegisteredOutput(t, svc, flags...)
				wantExit := 0
				if status == service.DoctorFail {
					wantExit = 2
				}
				assert.Equal(t, wantExit, exitCodeForError(err))
				assert.Equal(t, 1, strings.Count(text, "extra.check"))
				if jsonFlag {
					rows := doctorCLIRows(t, text)
					require.Len(t, rows, 1)
					assert.Equal(t, string(status), rows[0]["status"])
				} else {
					assert.Contains(t, text, "["+strings.ToUpper(string(status))+"]")
					assert.Contains(t, text, "one remedy")
				}
			})
		}
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestDoctorFix_GrantCheck_PrintsCommandOnly(t *testing.T) {
	for _, kind := range []service.DoctorRepairKind{service.DoctorRepairNone, service.DoctorRepairGrant, service.DoctorRepairKind(99)} {
		svc := service.NewDoctorService(service.DoctorOptions{})
		calls := 0
		require.NoError(t, svc.Register(service.DoctorCheck{ID: "grant", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorFail, Fix: "mtix agent allow worker"}
		}, Fixer: &service.DoctorFixer{Kind: kind, Apply: func(context.Context) error { calls++; return nil }}}))
		text, err := doctorRegisteredOutput(t, svc, "--fix")
		assert.ErrorIs(t, err, service.ErrDoctorChecksFailed)
		assert.Equal(t, 0, calls)
		assert.Contains(t, text, "mtix agent allow worker")
		assert.Contains(t, text, "automatic repair refused")
	}
}

func TestDoctor_FinalCollectorAndRepairFieldsRedactedInBothFormats(t *testing.T) {
	const secret = "postgres://fixture:doctor_adapter_secret@/%zz"
	for _, flags := range [][]string{{"--fix"}, {"--fix", "--json"}} {
		svc := service.NewDoctorService(service.DoctorOptions{Scrub: func(s string) string { return redact.Known(s, secret) }, CollectSync: func(context.Context) ([]service.DoctorResult, error) {
			return []service.DoctorResult{{ID: "sync." + secret, Status: service.DoctorWarn, Detail: secret, Fix: secret}}, nil
		}})
		require.NoError(t, svc.Register(service.DoctorCheck{ID: "repair", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorFail, Detail: secret, Fix: secret}
		}, Fixer: &service.DoctorFixer{Kind: service.DoctorRepairNonGrant, Apply: func(context.Context) error { return errors.New(secret) }}}))
		text, err := doctorRegisteredOutput(t, svc, flags...)
		assert.ErrorIs(t, err, service.ErrDoctorChecksFailed)
		assert.NotContains(t, text, secret)
		assert.NotContains(t, text, "doctor_adapter_secret")
		assert.Contains(t, text, "REDACTED")
		assert.Contains(t, text, "repair failed")
	}
}

type doctorBrokenWriter struct{ cause error }

func (w doctorBrokenWriter) Write([]byte) (int, error) { return 0, w.cause }

func TestDoctor_WriteFailures_KeepCauseAndRedactError(t *testing.T) {
	cause := errors.New("postgres://fixture:writer_secret@localhost/db")
	for _, jsonFlag := range []bool{false, true} {
		svc := service.NewDoctorService(service.DoctorOptions{})
		require.NoError(t, svc.Register(service.DoctorCheck{ID: "row", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorPass, Fix: "fix"}
		}}))
		err := runDoctor(t.Context(), doctorBrokenWriter{cause}, svc, doctorOutputOptions{JSON: jsonFlag})
		assert.ErrorIs(t, err, cause)
		assert.NotContains(t, err.Error(), "writer_secret")
	}
}

func TestDoctor_Cancellation_NoCallbacksAndExitOne(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc := service.NewDoctorService(service.DoctorOptions{})
	var out bytes.Buffer
	err := runDoctor(ctx, &out, svc, doctorOutputOptions{JSON: true})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, exitCodeForError(err))
}

func TestDoctor_InitializationError_RedactedWithCause(t *testing.T) {
	testutil.OperatorState(t)
	saveAndResetApp(t)
	t.Chdir(t.TempDir())
	const secret = "postgres://fixture:initialize_secret@/%zz"
	t.Setenv("MTIX_SYNC_DSN", secret)
	cause := errors.New(secret)
	cmd := newDoctorCommand(func() (*service.DoctorService, error) { return nil, cause })
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(t.Context())
	assert.ErrorIs(t, err, cause)
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), "initialize_secret")
}
