// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests sender policy parsing and conservative attribution of legacy charges.
package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestWakeSenderLimit_Values(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    int64
		invalid bool
	}{
		{"", 0, false}, {"1", 1, false}, {"0", 0, true}, {"-1", 0, true}, {"invalid", 0, true}, {"9223372036854775808", 0, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := wakeSenderLimit(tc.value)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.invalid, err != nil)
		})
	}
}

func TestWakeSenderUsed_UnknownChargesProtectEverySender(t *testing.T) {
	m := &oplocal.WakeBudgetMirror{Credits: []oplocal.WakeBudgetCredit{
		{Sender: "a", Count: 2}, {Sender: "b", Count: 3}, {Count: 4},
	}}
	for _, tc := range []struct {
		sender string
		want   int64
	}{{"a", 6}, {"b", 7}, {"c", 4}, {"", 9}} {
		t.Run(tc.sender, func(t *testing.T) { require.Equal(t, tc.want, wakeSenderUsed(m, tc.sender)) })
	}
}
