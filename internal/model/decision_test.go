// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func decisionFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../docs/schemas/fixtures/" + name + ".json")
	require.NoError(t, err)
	return b
}

func TestDecisionModel_NonTestDependencies_ExcludeXText(t *testing.T) {
	// Without -test, go list checks only the production package and its dependencies.
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, string(out))

	const module = "golang.org/x/text"
	for _, dep := range strings.Fields(string(out)) {
		if dep == module || strings.HasPrefix(dep, module+"/") {
			t.Errorf("internal/model must not depend on %s", dep)
		}
	}
}

func TestDecisionCanonical_TransportVectors(t *testing.T) {
	var hashes struct{ Payload, Identity string }
	require.NoError(t, json.Unmarshal(decisionFixture(t, "request-hashes"), &hashes))
	var first DecisionPayload
	for _, name := range []string{"request", "request-duplicates"} {
		d, err := DecodeDecision(decisionFixture(t, name), "asker", false)
		require.NoError(t, err)
		require.Equal(t, hashes.Payload, d.PayloadSHA256)
		require.Equal(t, hashes.Identity, d.IdentitySHA256)
		require.Equal(t, name == "request-duplicates", d.Duplicates)
		if name == "request" {
			first = d.Payload
		} else {
			require.Equal(t, first, d.Payload)
		}
		_, err = DecodeDecision(decisionFixture(t, name), "asker", true)
		require.Equal(t, name == "request-duplicates", errors.Is(err, ErrInvalidInput))
	}
	require.True(t, first.RequiresOutcome)
	require.Equal(t, "user", first.AnswerableBy)
	require.Equal(t, "agent", first.Asker.Kind)
}

func TestDecisionCanonical_RFC8785(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"":0,"😀":1,"s":"\u003c\u000f\n","n":[-0,1e30,0.0000001,333333333.33333329,1e-6]}`, `{"n":[0,1e+30,1e-7,333333333.3333333,0.000001],"s":"<\u000f\n","😀":1,"":0}`},
		// RFC 8785 section 3.2.2.3: plain digits below 1e21, exponent form from 1e21.
		{`{"n":[1e21,999999999999999900000,1e20,-1e21]}`, `{"n":[1e+21,999999999999999900000,100000000000000000000,-1e+21]}`},
	} {
		b, _, err := canonicalDecision([]byte(tc.in), false)
		require.NoError(t, err)
		require.Equal(t, tc.want, string(b))
	}
	for _, s := range []string{"", `[]`, `{}{}`, `{"a":1e999,"a":1}`, `{"s":"\ud800"}`, `{"s":"\udfff"}`, "{\"s\":\"\xff\"}", strings.Repeat("[", 11) + strings.Repeat("]", 11), `{"a":}`} {
		_, _, err := canonicalDecision([]byte(s), false)
		require.ErrorIs(t, err, ErrInvalidInput, s)
	}
}

func TestDecisionTTYSession_Vectors(t *testing.T) {
	var cases []struct {
		Token string
		Valid bool
	}
	require.NoError(t, json.Unmarshal(decisionFixture(t, "tty-session-vectors"), &cases))
	for _, tc := range cases {
		t.Run(tc.Token, func(t *testing.T) {
			b := strings.TrimSuffix(strings.TrimSpace(string(decisionFixture(t, "answer"))), "}") + `,"evidence":{"tty_session":` + tc.Token + "}}"
			for _, author := range []bool{false, true} {
				d, err := DecodeDecision([]byte(b), "user", author)
				if tc.Valid {
					require.NoError(t, err)
					require.NotNil(t, d.Payload.Evidence.TTYSession)
				} else {
					require.ErrorIs(t, err, ErrInvalidInput)
				}
			}
		})
	}
}

func TestDecisionFallback_CapAndSuffix(t *testing.T) {
	for _, q := range []string{"Proceed?", strings.Repeat("界", 2048)} {
		b, err := DecisionFallback("d-12345678", "T-1", "asker", q)
		require.NoError(t, err)
		require.LessOrEqual(t, len(b), 2048)
		require.True(t, utf8.ValidString(b))
		require.Contains(t, b, "DECISION NEEDED (d-12345678)")
		require.True(t, strings.HasSuffix(b, "Answer with `mtix decisions answer d-12345678` (needs mtix 0.7.0 or later), or reply with `mtix comment T-1 --to asker \"…\"`."))
	}
	_, err := DecisionFallback("d-short", strings.Repeat("x", 2048), "asker", "?")
	require.ErrorIs(t, err, ErrInvalidInput)
}

// TestDecisionAuthorPath_ExcessPrecisionNumber_IsRefused pins I-JSON number
// strictness on the author path (ADR-008 section 3.2, MTIX-219.4): a number
// token that a double cannot hold exactly as written is refused, never rounded.
// The ingest path keeps accepting it, because a replica must not fork on
// events already in the journal.
func TestDecisionAuthorPath_ExcessPrecisionNumber_IsRefused(t *testing.T) {
	for _, tc := range []struct {
		token string
		valid bool
	}{
		{"3.141592653589793238462643383279", false},
		{"9007199254740993", false},
		{"-9007199254740993", false},
		{"0.1000000000000000055511151231257827", false},
		{"1e400", false},
		{"1e-400", false},
		{"1e-99999999999", false},
		{"9007199254740992", true},
		{"9007199254740991", true},
		{"0.1", true},
		{"1.50", true},
		{"-0", true},
		{"1e21", true},
		{"123e-2", true},
		{"3.141592653589793", true},
	} {
		in := []byte(`{"n":` + tc.token + `}`)
		_, _, err := canonicalDecision(in, true)
		if tc.valid {
			require.NoError(t, err, tc.token)
		} else {
			require.ErrorIs(t, err, ErrInvalidInput, tc.token)
		}
		if tc.token != "1e400" && tc.token != "1e-400" && tc.token != "1e-99999999999" {
			_, _, err = canonicalDecision(in, false)
			require.NoError(t, err, "ingest "+tc.token)
		}
	}
}

// TestDecisionWireValues_UserRename_AcceptsOnlyUserAndAny pins the DESK-1.393
// rename: answerable_by takes exactly user or any, asker.kind takes user or
// agent, and the retired word, other cases and qualified forms are refused.
func TestDecisionWireValues_UserRename_AcceptsOnlyUserAndAny(t *testing.T) {
	for _, tc := range []struct {
		path, value string
		valid       bool
	}{
		{"answerable_by", "user", true}, {"answerable_by", "any", true},
		{"answerable_by", "human", false}, {"answerable_by", "User", false},
		{"answerable_by", "user:alice", false}, {"answerable_by", "", false},
		{"asker.kind", "user", true}, {"asker.kind", "agent", true},
		{"asker.kind", "human", false},
	} {
		_, err := DecodeDecision(decisionMutation(t, "request", tc.path, tc.value), "user", true)
		if tc.valid {
			require.NoError(t, err, tc.path+"="+tc.value)
		} else {
			require.Error(t, err, tc.path+"="+tc.value)
		}
	}
}
