// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSplitCSV verifies comma-separated flag value parsing per FR-17.1.
// All edge cases agents may hit when supplying multi-value filters.
func TestSplitCSV(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string returns nil", "", nil},
		{"single value returns one element", "PROJ-1", []string{"PROJ-1"}},
		{"two values", "PROJ-1,PROJ-2", []string{"PROJ-1", "PROJ-2"}},
		{"three values", "a,b,c", []string{"a", "b", "c"}},
		{"trailing space stripped", "a ,b ,c ", []string{"a", "b", "c"}},
		{"leading space stripped", " a, b, c", []string{"a", "b", "c"}},
		{"both sides stripped", "  a  ,  b  ", []string{"a", "b"}},
		{"empty between commas skipped", "a,,b", []string{"a", "b"}},
		{"trailing comma skipped", "a,b,", []string{"a", "b"}},
		{"leading comma skipped", ",a,b", []string{"a", "b"}},
		{"only commas returns nil", ",,,", nil},
		{"only spaces returns nil", "   ", nil},
		{"whitespace-only elements skipped", "a, ,b", []string{"a", "b"}},
		{"single value with spaces", "  PROJ-1  ", []string{"PROJ-1"}},
		{"tabs treated as whitespace", "a\t,\tb", []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitCSV(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestSplitCSVInts verifies parsing comma-separated integers per FR-17.1.
// Used for --priority filter.
func TestSplitCSVInts(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []int
		wantErr bool
	}{
		{"empty string returns nil", "", nil, false},
		{"single int", "1", []int{1}, false},
		{"multiple ints", "1,2,3", []int{1, 2, 3}, false},
		{"with spaces", " 1 , 2 , 3 ", []int{1, 2, 3}, false},
		{"empty elements skipped", "1,,2", []int{1, 2}, false},
		{"non-numeric returns error", "1,foo,3", nil, true},
		{"float returns error", "1.5", nil, true},
		{"negative ok", "-1", []int{-1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitCSVInts(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
