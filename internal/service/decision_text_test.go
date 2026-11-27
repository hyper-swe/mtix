// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCombiningGraphemeJoiner(t *testing.T) {
	require.Equal(t, "\u034f", combiningGraphemeJoiner)
}

func TestNormalizeDecisionText_TrimAndNFC(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"decomposed equals composed", "é", "é"},
		{"composed is unchanged", "é", "é"},
		{"trim and compose", "  É  x\n", "É  x"},
		{"interior white space kept", "a  b", "a  b"},
		{"empty", "", ""},
		{"only white space", " \t\n", ""},
		{"explicit joiner kept", "a͏́", "a͏́"},
		{"thirty marks accepted", "a" + strings.Repeat("́", 30), "á" + strings.Repeat("́", 29)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDecisionText(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestNormalizeDecisionText_NeverRewritesToAJoiner pins that a text with a run
// of 31 combining marks is refused rather than silently equated with the same
// text carrying an explicit U+034F.
func TestNormalizeDecisionText_NeverRewritesToAJoiner(t *testing.T) {
	run := "a" + strings.Repeat("́", 31)
	_, err := NormalizeDecisionText(run)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	explicit := "a" + strings.Repeat("́", 30) + "͏́"
	got, err := NormalizeDecisionText(explicit)
	require.NoError(t, err)
	require.Contains(t, got, "͏")
	_, err = NormalizeDecisionText("x" + run)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func decodeDecisionFixture(t *testing.T, name string) model.DecisionPayload {
	t.Helper()
	b, err := os.ReadFile("../../docs/schemas/fixtures/" + name + ".json")
	require.NoError(t, err)
	d, err := model.DecodeDecision(b, "asker", false)
	require.NoError(t, err)
	return d.Payload
}

func TestValidateDecisionAnswer_ComparesNormalizedText(t *testing.T) {
	request := decodeDecisionFixture(t, "request")
	for _, tc := range []struct {
		name, input, note string
		valid             bool
	}{
		{"decomposed input and padded note", " é ", " ok ", true},
		{"note blank after trim", "x", "   ", false},
		{"input blank after trim", "\n", "n", false},
		{"input with a combining run", "a" + strings.Repeat("́", 31), "n", false},
		{"note with a combining run", "x", "a" + strings.Repeat("́", 31), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := model.DecisionPayload{DecisionAnswer: &model.DecisionAnswer{
				Option: "yes", InputValue: tc.input, Note: tc.note, OpenedLinks: []int{0},
			}}
			err := ValidateDecisionAnswer(request, answer)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, model.ErrInvalidInput)
			}
		})
	}
}
