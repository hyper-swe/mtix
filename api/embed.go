// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package api embeds the OpenAPI specification for serving at runtime per FR-16.4.
package api

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
)

//go:embed openapi.yaml
var specYAML []byte

// specHash is the SHA-256 hex digest of the embedded spec per FR-16.8.
var specHash string

func init() {
	h := sha256.Sum256(specYAML)
	specHash = fmt.Sprintf("%x", h)
}

// OpenAPIYAML returns the embedded OpenAPI 3.1 spec as raw YAML bytes.
func OpenAPIYAML() []byte {
	return specYAML
}

// OpenAPISpecHash returns the SHA-256 hex digest of the spec per FR-16.8.
func OpenAPISpecHash() string {
	return specHash
}
