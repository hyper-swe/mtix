// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// defaultPrefix is the fallback primary project prefix used when config is
// unavailable. It mirrors the config default per FR-11.2.
const defaultPrefix = "PROJ"

// primaryProject returns the configured primary project prefix per
// FR-MULTI-PROJECT. Falls back to defaultPrefix if config is unavailable.
func (s *Server) primaryProject() string {
	if v, err := s.configSvc.Get("prefix"); err == nil && v != "" {
		return v
	}
	return defaultPrefix
}

// resolveProjectScope maps the ?project query param to a store.NodeFilter
// Project value per FR-MULTI-PROJECT MP-9:
//   - omitted/empty  → the primary project (config prefix)
//   - "all"          → "" (no filter; spans all projects)
//   - any other value → that exact prefix
func (s *Server) resolveProjectScope(c *gin.Context) string {
	switch raw := strings.TrimSpace(c.Query("project")); raw {
	case "":
		return s.primaryProject()
	case "all":
		return ""
	default:
		return raw
	}
}

// listProjects handles GET /api/v1/projects per FR-MULTI-PROJECT MP-10.
// Returns a JSON array of {prefix, count, isPrimary} describing every project
// present in the store, with isPrimary set on the configured primary.
func (s *Server) listProjects(c *gin.Context) {
	infos, err := s.store.DistinctProjects(c.Request.Context())
	if err != nil {
		HandleError(c, err)
		return
	}

	primary := s.primaryProject()

	type projectView struct {
		Prefix    string `json:"prefix"`
		Count     int    `json:"count"`
		IsPrimary bool   `json:"isPrimary"`
	}

	out := make([]projectView, 0, len(infos))
	for _, info := range infos {
		out = append(out, projectView{
			Prefix:    info.Prefix,
			Count:     info.Count,
			IsPrimary: info.Prefix == primary,
		})
	}

	c.JSON(http.StatusOK, out)
}
