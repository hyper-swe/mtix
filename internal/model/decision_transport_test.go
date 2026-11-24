// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// decisionWithExtension appends one unknown member to a fixture object.
func decisionWithExtension(t *testing.T, fixture, member string) []byte {
	t.Helper()
	b := strings.TrimSuffix(strings.TrimSpace(string(decisionFixture(t, fixture))), "}")
	return []byte(b + "," + member + "}")
}

// TestDecisionTransport_CompactAndExpandedNumbersConverge pins that one
// logical value is judged the same whether a transport keeps exponent
// spellings (the relay) or expands them (a JSONB round trip).
func TestDecisionTransport_CompactAndExpandedNumbersConverge(t *testing.T) {
	const n = 3000
	compact := "1e300" + strings.Repeat(",1e300", n-1)
	expanded := "1" + strings.Repeat("0", 300) + strings.Repeat(",1"+strings.Repeat("0", 300), n-1)
	small := decisionWithExtension(t, "request", `"x":[`+compact+`]`)
	large := decisionWithExtension(t, "request", `"x":[`+expanded+`]`)
	require.Greater(t, len(large), 64<<10, "the expanded spelling exceeds the canonical cap as raw text")
	require.Less(t, len(small), 64<<10)
	for _, authoring := range []bool{false, true} {
		a, err := DecodeDecision(small, "asker", authoring)
		require.NoError(t, err)
		b, err := DecodeDecision(large, "asker", authoring)
		require.NoError(t, err)
		require.Equal(t, a.PayloadSHA256, b.PayloadSHA256)
		require.Equal(t, a.IdentitySHA256, b.IdentitySHA256)
		require.Equal(t, a.Payload, b.Payload)
	}
}

// TestDecisionTransport_CanonicalSizeBoundary pins the 64 KiB cap on the
// canonical value, and the separate raw-input sanity bound.
func TestDecisionTransport_CanonicalSizeBoundary(t *testing.T) {
	base, _, err := canonicalDecision(decisionWithExtension(t, "request", `"x":""`), false)
	require.NoError(t, err)
	pad := 64<<10 - len(base)
	for _, tc := range []struct {
		name  string
		extra int
		valid bool
	}{{"at cap", 0, true}, {"one over", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := decisionWithExtension(t, "request", `"x":"`+strings.Repeat("a", pad+tc.extra)+`"`)
			for _, authoring := range []bool{false, true} {
				_, err := DecodeDecision(raw, "asker", authoring)
				if tc.valid {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrInvalidInput)
				}
			}
		})
	}
	huge := append([]byte(strings.Repeat(" ", 4<<20)), decisionFixture(t, "request")...)
	_, err = DecodeDecision(huge, "asker", false)
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestDecisionTransport_Noncharacters(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		valid       bool
	}{
		{"literal FDD0", "﷐", false},
		{"escaped FDD0", `﷐`, false},
		{"escaped FDEF", `﷯`, false},
		{"escaped FFFE", `￾`, false},
		{"literal FFFF", "￿", false},
		{"pair 1FFFE", `🿾`, false},
		{"pair 10FFFF", `􏿿`, false},
		{"below the block", `﷏`, true},
		{"above the block", `ﷰ`, true},
		{"replacement character", `�`, true},
		{"pair 1FFFD", `🿽`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, member := range []string{`"x":"` + tc.token + `"`, `"` + tc.token + `":1`, `"x":["` + tc.token + `"]`} {
				for _, authoring := range []bool{false, true} {
					_, err := DecodeDecision(decisionWithExtension(t, "request", member), "asker", authoring)
					if tc.valid {
						require.NoError(t, err, member)
					} else {
						require.ErrorIs(t, err, ErrInvalidInput, member)
					}
				}
			}
		})
	}
}

// TestDecisionOutcome_RefusalSetSurvivesRoundTrip pins that absent, empty and
// non-empty seen_answers decode the same way after the typed view is encoded.
func TestDecisionOutcome_RefusalSetSurvivesRoundTrip(t *testing.T) {
	const other = "01912345-6791-7abc-8123-123456789abc"
	answer := "01912345-6790-7abc-8123-123456789abc"
	for _, tc := range []struct {
		name string
		raw  []byte
		want []string
	}{
		{"absent", decisionFixture(t, "refused"), []string{answer}},
		{"empty", decisionMutation(t, "refused", "seen_answers", []string{}), []string{}},
		{"non-empty", decisionMutation(t, "refused", "seen_answers", []string{other}), []string{other}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, err := DecodeDecision(tc.raw, "asker", false)
			require.NoError(t, err)
			require.Equal(t, tc.want, first.Payload.SeenAnswers)
			encoded, err := json.Marshal(first.Payload)
			require.NoError(t, err)
			second, err := DecodeDecision(encoded, "asker", false)
			require.NoError(t, err, string(encoded))
			require.Equal(t, tc.want, second.Payload.SeenAnswers)
			require.Equal(t, first.Payload, second.Payload)
		})
	}
}

func TestDecisionTTYSession_DecodedValue(t *testing.T) {
	want := map[string]int64{"0": 0, "1": 1, "1.0": 1, "9007199254740991": 1<<53 - 1}
	var cases []struct {
		Token string
		Valid bool
	}
	require.NoError(t, json.Unmarshal(decisionFixture(t, "tty-session-vectors"), &cases))
	seen := 0
	for _, tc := range cases {
		if !tc.Valid {
			continue
		}
		seen++
		expected, ok := want[tc.Token]
		require.True(t, ok, "unpinned valid vector %s", tc.Token)
		b := decisionWithExtension(t, "answer", `"evidence":{"tty_session":`+tc.Token+`}`)
		for _, authoring := range []bool{false, true} {
			d, err := DecodeDecision(b, "user", authoring)
			require.NoError(t, err)
			require.NotNil(t, d.Payload.Evidence.TTYSession)
			require.Equal(t, expected, *d.Payload.Evidence.TTYSession, tc.Token)
		}
	}
	require.Equal(t, len(want), seen)
	d, err := DecodeDecision(decisionFixture(t, "answer"), "user", false)
	require.NoError(t, err)
	require.Nil(t, d.Payload.Evidence.TTYSession)
}

func TestDecisionFallback_KeepsQuestionPrefix(t *testing.T) {
	const prefix = "DECISION NEEDED (d-12345678): "
	suffix := "\nAnswer with `mtix decisions answer d-12345678` (needs mtix 0.7.0 or later), or reply with `mtix comment T-1 --to asker \"…\"`."
	short, err := DecisionFallback("d-12345678", "T-1", "asker", "Proceed?")
	require.NoError(t, err)
	require.Equal(t, prefix+"Proceed?"+suffix, short)
	budget := 2048 - len(prefix) - len(suffix)
	long, err := DecisionFallback("d-12345678", "T-1", "asker", strings.Repeat("界", 2048))
	require.NoError(t, err)
	require.Equal(t, prefix+strings.Repeat("界", budget/3)+suffix, long)
	exact, err := DecisionFallback("d-12345678", "T-1", "asker", strings.Repeat("a", budget))
	require.NoError(t, err)
	require.Equal(t, prefix+strings.Repeat("a", budget)+suffix, exact)
	require.Equal(t, 2048, len(exact), fmt.Sprint(len(exact)))
}
