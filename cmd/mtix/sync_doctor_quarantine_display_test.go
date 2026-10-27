// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Recovery hint display tests keep text presentation and JSON data independent.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type doctorDisplayCase struct {
	name, id string
	inspect  bool
}

func doctorDisplayCases() []doctorDisplayCase {
	cases := []doctorDisplayCase{
		{"ordinary", "evt-1", false},
		{"quoted", "evt-'quote", false},
		{"leading", "-evt-1", false},
		{"unicode", "evt-α🙂", false},
	}
	for r := rune(0); r <= 0x1f; r++ {
		cases = append(cases, doctorDisplayCase{fmt.Sprintf("format_%04x", r), "evt-" + string(r) + "-1", true})
	}
	for r := rune(0x7f); r <= 0x9f; r++ {
		cases = append(cases, doctorDisplayCase{fmt.Sprintf("format_%04x", r), "evt-" + string(r) + "-1", true})
	}
	for _, r := range []rune{'\u2028', '\u2029', '\u202e', '\u200b', '\u2060', '\u2066', '\u00a0'} {
		cases = append(cases, doctorDisplayCase{fmt.Sprintf("format_%04x", r), "evt-" + string(r) + "-1", true})
	}
	cases = append(cases,
		doctorDisplayCase{"ascii_space", "evt-a b", false},
		doctorDisplayCase{"printable_mark", "evt-e\u0301", false},
	)
	return cases
}

// expectedDoctorDisplayHint pins the required-explanation command independently
// of the formatter under test, including its event-id quoting.
func expectedDoctorDisplayHint(id string) string {
	return "Inspect and report retained events (1 eligible unacknowledged; first shown); add --reason '<your explanation>' before --, then: mtix sync quarantine acknowledge -- '" + strings.ReplaceAll(id, "'", "'\"'\"'") + "'"
}

func TestDoctorRecoveryHint_DisplayContract(t *testing.T) {
	for _, tc := range doctorDisplayCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			initTestApp(t) // Includes an exclusively owned operator configuration directory.
			seedDoctorFixCandidate(t, tc.id, "pull")
			for _, jsonOutput := range []bool{false, true} {
				t.Run(map[bool]string{false: "text", true: "json"}[jsonOutput], func(t *testing.T) {
					app.jsonOutput = jsonOutput
					report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
					require.Len(t, report.Checks, 1)
					require.False(t, report.Checks[0].Pass)
					if jsonOutput {
						assertDoctorDisplayJSON(t, report, tc.id)
						return
					}
					fix := report.Checks[0].Fix
					if tc.inspect {
						assertDoctorInspectHint(t, tc.id, fix)
					} else {
						require.Equal(t, expectedDoctorDisplayHint(tc.id), fix)
					}
					var out bytes.Buffer
					printDoctorTable(&out, report)
					require.Contains(t, out.String(), "\n       fix: "+fix+"\n")
				})
			}
		})
	}
}

func assertDoctorDisplayJSON(t *testing.T, report DoctorReport, id string) {
	t.Helper()
	require.Equal(t, expectedDoctorDisplayHint(id), report.Checks[0].Fix)
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	var parsed struct {
		Checks []struct {
			Fix json.RawMessage `json:"fix"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))
	require.Len(t, parsed.Checks, 1)
	want, err := json.Marshal(expectedDoctorDisplayHint(id))
	require.NoError(t, err)
	require.Equal(t, want, []byte(parsed.Checks[0].Fix))
}

func assertDoctorInspectHint(t *testing.T, id, fix string) {
	t.Helper()
	require.Contains(t, fix, "mtix sync quarantine list --json")
	require.Contains(t, fix, strconv.QuoteToASCII(id))
	require.NotContains(t, fix, "mtix sync quarantine acknowledge")
	for _, r := range fix {
		if unicode.IsControl(r) {
			t.Fatalf("text hint contains a non-display value: %U", r)
		}
	}
}

func TestDoctorRecoveryHint_ByteDisplay(t *testing.T) {
	for n, id := range []string{"evt-\x85", "evt-\xff"} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			fix := quarantineAcknowledgementCommand(id, 1, false)
			assertDoctorInspectHint(t, id, fix)
		})
	}
}
