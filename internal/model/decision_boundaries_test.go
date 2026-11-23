// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidDecisionURL_SchemeAndHostMatrix_OnlyAllowedFormsPass(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://example.com/a", true},
		{"HTTPS://example.com/a", true},
		{"http://localhost/a", true},
		{"http://LOCALHOST:8080/a", true},
		{"http://127.0.0.1:8080/a", true},
		{"http://[::1]:8080/a", true},
		{"HtTp://localhost/a", true},
		{"hTTps://example.com/a", true},
		{"http://example.com/a", false},
		{"ftp://localhost/a", false},
		{"ftp://127.0.0.1/a", false},
		{"FTP://localhost/a", false},
		{"Ftp://LocalHost/a", false},
		{"javascript://localhost/a", false},
		{"JavaScript://127.0.0.1/a", false},
		{"data://localhost/a", false},
		{"DATA://127.0.0.1/a", false},
		{"file://localhost/etc/passwd", false},
		{"File://127.0.0.1/etc/passwd", false},
		{"ws://localhost/a", false},
		{"https://example.com\\a", false},
		{"https:\\\\example.com/a", false},
		{"http://localhost\\a", false},
		{"http://127.0.0.1/a\\b", false},
		{"http://localhost/\\evil.com", false},
		{"https://example.com/a\\", false},
		{"/relative", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			require.Equal(t, tt.want, validDecisionURL(tt.url))
		})
	}
}

func TestSingleLine_UnicodeLineBreaks_Rejected(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plain", "one line", true},
		{"lf", "a\nb", false},
		{"cr", "a\rb", false},
		{"nel U+0085", "a\u0085b", false},
		{"line separator U+2028", "a b", false},
		{"paragraph separator U+2029", "a b", false},
		{"U+2027 neighbour", "a‧b", true},
		{"U+202A neighbour", "a‪b", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, singleLine(tt.in))
			require.Equal(t, tt.want, decisionQuestion(tt.in))
			require.Equal(t, tt.want, decisionConsequence(tt.in))
		})
	}
}

func TestDecisionFallback_BudgetBoundary_ExactPassesOneOverCut(t *testing.T) {
	const prefix = "DECISION NEEDED (d-12345678): "
	suffix := "\nAnswer with `mtix decisions answer d-12345678` (needs mtix 0.7.0 or later), or reply with `mtix comment T-1 --to asker \"…\"`."
	budget := 2048 - len(prefix) - len(suffix)
	tests := []struct {
		name    string
		n       int
		wantLen int
	}{
		{"budget minus one", budget - 1, 2047},
		{"exactly budget", budget, 2048},
		{"budget plus one is cut", budget + 1, 2048},
		{"budget plus two is cut", budget + 2, 2048},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecisionFallback("d-12345678", "T-1", "asker", strings.Repeat("a", tt.n))
			require.NoError(t, err)
			require.Len(t, got, tt.wantLen)
			want := tt.n
			if want > budget {
				want = budget
			}
			require.Equal(t, prefix+strings.Repeat("a", want)+suffix, got)
		})
	}
}
