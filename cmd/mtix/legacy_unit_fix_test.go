// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The dispatcher.legacy-unit-scan doctor fix is one runnable command that survives hostile paths.
package main

import (
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

var unreadableFixCases = []struct {
	name  string
	paths []string
	want  string
}{
	{"one plain path", []string{"/u/a.service"}, "ls -l -- '/u/a.service'"},
	{"path with spaces", []string{"/u/my dir/a.service"}, "ls -l -- '/u/my dir/a.service'"},
	{"path with apostrophe", []string{"/u/o'brien/a.service"}, `ls -l -- '/u/o'\''brien/a.service'`},
	{"path of only apostrophes", []string{"''"}, `ls -l -- ''\'''\'''`},
	{"several paths", []string{"/u/a.service", "/u/b c.service", "/u/it's.service"},
		`ls -l -- '/u/a.service' '/u/b c.service' '/u/it'\''s.service'`},
	{"leading dash path", []string{"-rf"}, "ls -l -- '-rf'"},
}

func TestUnreadableUnitFix_HostilePaths_ExactQuotedCommand(t *testing.T) {
	for _, tc := range unreadableFixCases {
		t.Run(tc.name, func(t *testing.T) {
			got := unreadableUnitFix(tc.paths)
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, ";", "no prose after a semicolon in the runnable line")
			require.NotContains(t, got, "operator", "the operator decision is not part of the command")
		})
	}
}

// A shell must read the fix back as exactly ls, -l, -- and the original paths: the apostrophe
// prose that broke the old line would be a syntax error here and the stub ls would never run.
func TestUnreadableUnitFix_ShellRoundTrip_ArgvEqualsPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipped on windows: the fix line is a POSIX sh command")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("skipped: sh is not on PATH, so the shell round trip cannot run")
	}
	for _, tc := range unreadableFixCases {
		t.Run(tc.name, func(t *testing.T) {
			// A shell function named ls shadows the program and prints each argument NUL-terminated.
			script := `ls() { for a in "$@"; do printf '%s\0' "$a"; done; }; ` + unreadableUnitFix(tc.paths)
			out, runErr := exec.Command(shPath, "-c", script).Output() //nolint:gosec // G204: the script is built from test constants
			require.NoError(t, runErr)
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			require.Equal(t, append([]string{"-l", "--"}, tc.paths...), got)
		})
	}
}

func TestRegisterUnreadableUnitDoctor_ApostropheAndManyPaths_FixAloneAndJSONData(t *testing.T) {
	paths := []string{"/u/it's.service", "/u/a b.service"}
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerUnreadableUnitDoctor(svc, paths))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, `ls -l -- '/u/it'\''s.service' '/u/a b.service'`, row.Fix)
	require.Contains(t, row.Detail, "operator decision", "the operator decision is in the detail")
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var decoded struct {
		Fix  string `json:"fix"`
		Data struct {
			Units []string `json:"unreadable_units"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, row.Fix, decoded.Fix)
	require.Equal(t, paths, decoded.Data.Units)
}
