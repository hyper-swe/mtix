// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// createNode handles POST /api/v1/nodes per FR-7.2.
func (s *Server) createNode(c *gin.Context) {
	var req struct {
		Title       string   `json:"title" binding:"required"`
		ParentID    string   `json:"parent_id"`
		Project     string   `json:"project"`
		Description string   `json:"description"`
		Prompt      string   `json:"prompt"`
		Acceptance  string   `json:"acceptance"`
		Priority    int      `json:"priority"`
		Labels      []string `json:"labels"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "invalid request body: "+err.Error())
		return
	}

	if req.Project == "" {
		if v, err := s.configSvc.Get("prefix"); err == nil {
			req.Project = v
		} else {
			req.Project = "PROJ"
		}
	}

	node, err := s.nodeSvc.CreateNode(c.Request.Context(), &service.CreateNodeRequest{
		Title:       req.Title,
		ParentID:    req.ParentID,
		Project:     req.Project,
		Description: req.Description,
		Prompt:      req.Prompt,
		Acceptance:  req.Acceptance,
		Priority:    model.Priority(req.Priority),
		Labels:      req.Labels,
		Creator:     c.GetHeader("X-Agent-ID"),
	})
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusCreated, node)
}

// getNode handles GET /api/v1/nodes/:id per FR-7.2.
func (s *Server) getNode(c *gin.Context) {
	nodeID := c.Param("id")

	node, err := s.nodeSvc.GetNode(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, node)
}

// updateNode handles PATCH /api/v1/nodes/:id per FR-7.2.
// Accepts partial updates — only non-null fields are applied.
func (s *Server) updateNode(c *gin.Context) {
	nodeID := c.Param("id")

	var req struct {
		Title       *string         `json:"title"`
		Description *string         `json:"description"`
		Prompt      *string         `json:"prompt"`
		Acceptance  *string         `json:"acceptance"`
		Priority    *model.Priority `json:"priority"`
		Labels      []string        `json:"labels"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "invalid request body: "+err.Error())
		return
	}

	updates := &store.NodeUpdate{
		Title:       req.Title,
		Description: req.Description,
		Prompt:      req.Prompt,
		Acceptance:  req.Acceptance,
		Priority:    req.Priority,
		Labels:      req.Labels,
	}

	if err := s.nodeSvc.UpdateNode(c.Request.Context(), nodeID, updates); err != nil {
		HandleError(c, err)
		return
	}

	// Return the updated node.
	node, err := s.nodeSvc.GetNode(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, node)
}

// deleteNode handles DELETE /api/v1/nodes/:id per FR-7.2.
// Supports ?cascade=true for cascading soft-delete.
func (s *Server) deleteNode(c *gin.Context) {
	nodeID := c.Param("id")
	cascade := c.Query("cascade") == "true"
	deletedBy := c.GetHeader("X-Agent-ID")
	if deletedBy == "" {
		deletedBy = "api"
	}

	if err := s.nodeSvc.DeleteNode(c.Request.Context(), nodeID, cascade, deletedBy); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "deleted": true, "cascade": cascade})
}

// getActivity handles GET /api/v1/nodes/:id/activity per FR-3.6.
// Returns activity entries with optional ?limit and ?offset pagination.
func (s *Server) getActivity(c *gin.Context) {
	nodeID := c.Param("id")

	limit := 50
	offset := 0
	if v := c.Query("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if v := c.Query("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	entries, err := s.store.GetActivity(c.Request.Context(), nodeID, limit, offset)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"entries": entries,
		"total":   len(entries),
	})
}

// getChildren handles GET /api/v1/nodes/:id/children per FR-7.2.
// Supports pagination via ?limit and ?offset query params.
func (s *Server) getChildren(c *gin.Context) {
	nodeID := c.Param("id")

	children, err := s.store.GetDirectChildren(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"children": children,
		"total":    len(children),
	})
}

// decomposeNode handles POST /api/v1/nodes/:id/decompose per FR-7.2.
// Creates multiple child nodes under the specified parent.
func (s *Server) decomposeNode(c *gin.Context) {
	parentID := c.Param("id")

	var req struct {
		Children []struct {
			Title      string `json:"title" binding:"required"`
			Prompt     string `json:"prompt"`
			Acceptance string `json:"acceptance"`
		} `json:"children" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "invalid request body: "+err.Error())
		return
	}

	if len(req.Children) == 0 {
		HandleValidationError(c, "at least one child is required")
		return
	}

	// Get parent to determine project.
	parent, err := s.nodeSvc.GetNode(c.Request.Context(), parentID)
	if err != nil {
		HandleError(c, err)
		return
	}

	creator := c.GetHeader("X-Agent-ID")
	created := make([]*model.Node, 0, len(req.Children))
	for _, child := range req.Children {
		node, createErr := s.nodeSvc.CreateNode(c.Request.Context(), &service.CreateNodeRequest{
			Title:      child.Title,
			ParentID:   parentID,
			Project:    parent.Project,
			Prompt:     child.Prompt,
			Acceptance: child.Acceptance,
			Creator:    creator,
		})
		if createErr != nil {
			HandleError(c, createErr)
			return
		}
		created = append(created, node)
	}

	c.JSON(http.StatusCreated, gin.H{
		"parent_id": parentID,
		"children":  created,
		"total":     len(created),
	})
}
