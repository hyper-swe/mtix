// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncate_MultiByte_NeverSplitsRune(t *testing.T) {
	for _, character := range []string{"é", "界", "🙂"} {
		t.Run(character, func(t *testing.T) {
			got := Truncate(strings.Repeat(character, 101), 100)
			assert.True(t, utf8.ValidString(got))
			assert.Equal(t, strings.Repeat(character, 97)+"...", got)
		})
	}
}

func TestTruncate_BoundaryLengths(t *testing.T) {
	for _, character := range []string{"x", "界"} {
		for _, length := range []int{0, 96, 97, 98, 99, 100, 101} {
			t.Run(fmt.Sprintf("%s/%d", character, length), func(t *testing.T) {
				input := strings.Repeat(character, length)
				want := input
				if length > 100 {
					want = strings.Repeat(character, 97) + "..."
				}
				assert.Equal(t, want, Truncate(input, 100))
			})
		}
	}
	for _, limit := range []int{-1, 0, 1, 2, 3, 4} {
		t.Run(fmt.Sprintf("limit/%d", limit), func(t *testing.T) {
			want := "abcdef"
			if limit == 4 {
				want = "a..."
			}
			assert.Equal(t, want, Truncate("abcdef", limit))
		})
	}
}
