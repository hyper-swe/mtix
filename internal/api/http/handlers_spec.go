// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/api"
)

// handleOpenAPIYAML serves the OpenAPI 3.1 spec as YAML per FR-16.4.
// Does NOT require X-Requested-With header (read-only documentation).
func (s *Server) handleOpenAPIYAML(c *gin.Context) {
	c.Header("X-Spec-Hash", api.OpenAPISpecHash())
	c.Data(http.StatusOK, "application/yaml", api.OpenAPIYAML())
}

// handleOpenAPIJSON serves the OpenAPI spec with JSON content type per FR-16.4.
func (s *Server) handleOpenAPIJSON(c *gin.Context) {
	c.Header("X-Spec-Hash", api.OpenAPISpecHash())
	c.Data(http.StatusOK, "application/json", api.OpenAPIYAML())
}
