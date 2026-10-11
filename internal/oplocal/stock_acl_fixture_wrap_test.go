// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fixture decoder error checks use SYNTHETIC JSON exclusively. They preserve
// inspectable decoder causes without altering the genuine captured fixture.
package oplocal

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStockFixture_TrailingMalformedJSON_PreservesSyntaxError(t *testing.T) {
	valid, err := json.Marshal(syntheticStockFixture())
	require.NoError(t, err)
	malformed := append(bytes.Clone(valid), '!')
	_, err = parseStockFixture(malformed, fmt.Sprintf("%x", sha256.Sum256(malformed)))
	require.ErrorContains(t, err, "stock fixture must contain exactly one JSON object")
	var syntaxError *json.SyntaxError
	require.True(t, errors.As(err, &syntaxError), "trailing decoder rejection must preserve *json.SyntaxError: %v", err)
	require.NotNil(t, syntaxError)
	require.Greater(t, syntaxError.Offset, int64(len(valid)))
}

func TestStockFixture_ErrorWrapping_SYNTHETICControls(t *testing.T) {
	valid, err := json.Marshal(syntheticStockFixture())
	require.NoError(t, err)
	cases := []struct {
		name       string
		data       []byte
		wantSyntax bool
	}{
		{"healthy synthetic structure", valid, false},
		{"malformed first object", []byte("!"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseStockFixture(tc.data, fmt.Sprintf("%x", sha256.Sum256(tc.data)))
			if !tc.wantSyntax {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "decode stock fixture")
			var syntaxError *json.SyntaxError
			require.True(t, errors.As(err, &syntaxError))
			require.NotNil(t, syntaxError)
		})
	}
}
