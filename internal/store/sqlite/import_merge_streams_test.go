// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCarriesNodeColumns_SchemaVersions_ReadsOnlyFrom110 verifies which
// schema versions a merge import trusts to carry the node columns 1.1.0
// added (MTIX-95.31.1): 1.1.0 and later do; 1.0.x, an empty version and
// anything unparsable read as 1.0.0, so the local values are kept.
func TestCarriesNodeColumns_SchemaVersions_ReadsOnlyFrom110(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"1", false},
		{"1.0.0", false},
		{"1.0.9", false},
		{"1.1.0", true},
		{"1.1", true},
		{"1.12.3", true},
		{"2.0.0", true},
		{"0.9.0", false},
		{"x.y.z", false},
		{"1.x.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.want, carriesNodeColumns(tt.version))
		})
	}
}
