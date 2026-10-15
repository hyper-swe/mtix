// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor output tests cover write boundaries and stock-root diagnostic initialization.
package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

type doctorNthWriter struct {
	remaining int
	cause     error
}

func (w *doctorNthWriter) Write(p []byte) (int, error) {
	w.remaining--
	if w.remaining == 0 {
		return 0, w.cause
	}
	return len(p), nil
}

func TestDoctor_TextWriter_FixAndEmptyReportFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []service.DoctorResult
		nth  int
	}{{"empty", nil, 1}, {"fix", []service.DoctorResult{{ID: "row", Status: service.DoctorPass, Fix: "fix"}}, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("write failure")
			err := writeDoctorRows(&doctorNthWriter{remaining: tc.nth, cause: cause}, tc.rows, false)
			assert.ErrorIs(t, err, cause)
		})
	}
}

func TestDoctor_InvalidResult_EmitsOnlyExecutionError(t *testing.T) {
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, svc.Register(service.DoctorCheck{ID: "bad", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: "invalid"}
	}}))
	var out bytes.Buffer
	err := runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Empty(t, out.String(), "invalid internal results must not violate the wire enum")
}

func TestDoctor_DiagnosticInit_PreservesMirrorAndParsedFlags(t *testing.T) {
	rootDir := doctorCLIBoard(t, "")
	saveAndResetApp(t)
	t.Chdir(t.TempDir())
	config := filepath.Join(rootDir, ".mtix", "config.yaml")
	require.NoError(t, os.WriteFile(config, []byte("prefix: TEST\nsync:\n  auto_sync: true\n"), 0o600))
	mirror := filepath.Join(rootDir, ".mtix", "tasks.json")
	const sentinel = "invalid mirror must not be imported or rewritten"
	require.NoError(t, os.WriteFile(mirror, []byte(sentinel), 0o600))
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"-C", rootDir, "--log-level", "debug", "doctor", "--json"})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.NotNil(t, app.configSvc, "parsed -C must select the fixture before initialization")
	assert.True(t, app.logger.Enabled(t.Context(), slog.LevelDebug), "parsed log level is captured at execution")
	rows := doctorCLIRows(t, out.String())
	for _, row := range rows {
		if assertEmptyWakeDoctorInfo(t, row["id"], row["status"], row["detail"], row["fix"]) {
			continue
		}
		assert.Equal(t, "platform.windows", row["id"])
	}
	data, err := os.ReadFile(mirror)
	require.NoError(t, err)
	assert.Equal(t, sentinel, string(data))
}

func TestDoctorDocumentation_ExamplesParseAndReferenceMatches(t *testing.T) {
	root := newRootCmd()
	doc, remaining, err := root.Find([]string{"doctor"})
	require.NoError(t, err)
	require.Empty(t, remaining)
	require.NoError(t, doc.ParseFlags([]string{"--fix", "--json"}))
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	assert.Equal(t, cliReferenceSection(t, generatedHelpReference(t), doc.Name(), doc.Use), cliReferenceSection(t, string(data), doc.Name(), doc.Use))
}
