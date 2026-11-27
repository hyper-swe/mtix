// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProto_ProgressResponse_CarriesExclusionFields keeps the wire message in step.
func TestProto_ProgressResponse_CarriesExclusionFields(t *testing.T) {
	src := readProtoFile(t, "mtix/v1/types.proto")
	for _, f := range []string{"has_invalidated_descendants", "all_children_excluded", "excluded_count"} {
		assert.Contains(t, src, f)
	}
}
