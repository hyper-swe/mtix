// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAPIYAML_NotEmpty verifies the spec is embedded.
func TestOpenAPIYAML_NotEmpty(t *testing.T) {
	spec := OpenAPIYAML()
	require.NotEmpty(t, spec, "embedded OpenAPI spec should not be empty")
	assert.True(t, strings.Contains(string(spec), "openapi:"),
		"spec should contain openapi version")
}

// TestOpenAPIYAML_IsOpenAPI31 verifies the spec version.
func TestOpenAPIYAML_IsOpenAPI31(t *testing.T) {
	spec := string(OpenAPIYAML())
	assert.Contains(t, spec, `"3.1.0"`)
}

// TestOpenAPISpecHash_Is64CharHex verifies SHA-256 format per FR-16.8.
func TestOpenAPISpecHash_Is64CharHex(t *testing.T) {
	hash := OpenAPISpecHash()
	assert.Len(t, hash, 64, "SHA-256 hex should be 64 chars")
	assert.Regexp(t, `^[a-f0-9]{64}$`, hash)
}

// TestOpenAPIYAML_ContainsCoreSchemas verifies required components per FR-16.3.
func TestOpenAPIYAML_ContainsCoreSchemas(t *testing.T) {
	spec := string(OpenAPIYAML())
	schemas := []string{"Node:", "NodeList:", "CreateNodeRequest:", "UpdateNodeRequest:", "ErrorResponse:", "ContextChain:"}
	for _, s := range schemas {
		assert.Contains(t, spec, s, "spec should contain schema %s", s)
	}
}

// TestOpenAPIYAML_ContainsServerDefinition verifies parameterized server per FR-16.5.
func TestOpenAPIYAML_ContainsServerDefinition(t *testing.T) {
	spec := string(OpenAPIYAML())
	assert.Contains(t, spec, "localhost:{port}")
	assert.Contains(t, spec, "6849")
}
