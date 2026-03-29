// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"github.com/hyper-swe/mtix/internal/model"
)

// claimNode handles POST /api/v1/nodes/:id/claim per FR-10.4.
// Requires X-Agent-ID header. Supports {force: true} for stale reclaim.
func (s *Server) claimNode(c *gin.Context) {
	nodeID := c.Param("id")
	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		HandleValidationError(c, "X-Agent-ID header required for claim")
		return
	}

	var req struct {
		Force bool `json:"force"`
	}
	_ = c.ShouldBindJSON(&req)

	var err error
	if req.Force {
		threshold := s.configSvc.AgentStaleThreshold()
		err = s.store.ForceReclaimNode(c.Request.Context(), nodeID, agentID, threshold)
	} else {
		err = s.store.ClaimNode(c.Request.Context(), nodeID, agentID)
	}

	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "agent": agentID, "status": "claimed"})
}

// unclaimNode handles POST /api/v1/nodes/:id/unclaim per FR-10.4.
// Requires reason in body.
func (s *Server) unclaimNode(c *gin.Context) {
	nodeID := c.Param("id")
	var req struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "reason is required")
		return
	}

	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}
	if err := s.store.UnclaimNode(c.Request.Context(), nodeID, req.Reason, agentID); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "status": "unclaimed"})
}

// doneNode handles POST /api/v1/nodes/:id/done per FR-6.3.
func (s *Server) doneNode(c *gin.Context) {
	nodeID := c.Param("id")
	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)
	reason := req.Reason
	if reason == "" {
		reason = "marked done via API"
	}

	if err := s.nodeSvc.TransitionStatus(
		c.Request.Context(), nodeID, model.StatusDone, reason, agentID,
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "status": "done"})
}

// deferNode handles POST /api/v1/nodes/:id/defer per FR-3.8.
// Accepts optional {until} timestamp in body.
func (s *Server) deferNode(c *gin.Context) {
	nodeID := c.Param("id")

	var req struct {
		Until string `json:"until"`
	}
	_ = c.ShouldBindJSON(&req)

	if req.Until != "" {
		if _, err := time.Parse(time.RFC3339, req.Until); err != nil {
			HandleValidationError(c, "invalid until timestamp: must be ISO-8601")
			return
		}
	}

	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	if err := s.nodeSvc.TransitionStatus(
		c.Request.Context(), nodeID, model.StatusDeferred, "deferred via API", agentID,
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "status": "deferred"})
}

// cancelNode handles POST /api/v1/nodes/:id/cancel per FR-6.3.
// Requires reason in body. Supports {cascade: true}.
func (s *Server) cancelNode(c *gin.Context) {
	nodeID := c.Param("id")
	var req struct {
		Reason  string `json:"reason" binding:"required"`
		Cascade bool   `json:"cascade"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "reason is required")
		return
	}

	if err := s.store.CancelNode(
		c.Request.Context(), nodeID, req.Reason, "api", req.Cascade,
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id": nodeID, "status": "cancelled", "reason": req.Reason,
	})
}

// reopenNode handles POST /api/v1/nodes/:id/reopen per FR-6.3.
func (s *Server) reopenNode(c *gin.Context) {
	nodeID := c.Param("id")

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)
	reason := req.Reason
	if reason == "" {
		reason = "reopened via API"
	}

	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	if err := s.nodeSvc.TransitionStatus(
		c.Request.Context(), nodeID, model.StatusOpen, reason, agentID,
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "status": "open"})
}

// rerunNode handles POST /api/v1/nodes/:id/rerun per FR-6.3.
// Reopens a done/cancelled node for re-execution.
func (s *Server) rerunNode(c *gin.Context) {
	nodeID := c.Param("id")
	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	if err := s.nodeSvc.TransitionStatus(
		c.Request.Context(), nodeID, model.StatusOpen, "rerun via API", agentID,
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "status": "open", "rerun": true})
}

// blockNode handles POST /api/v1/nodes/:id/block per FR-6.3.
func (s *Server) blockNode(c *gin.Context) {
	nodeID := c.Param("id")
	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	if err := s.nodeSvc.TransitionStatus(
		c.Request.Context(), nodeID, model.StatusBlocked, "blocked via API", agentID,
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": nodeID, "status": "blocked"})
}

// commentNode handles POST /api/v1/nodes/:id/comment per FR-7.2.
// Adds a comment/annotation to a node.
func (s *Server) commentNode(c *gin.Context) {
	nodeID := c.Param("id")

	var req struct {
		Text string `json:"text" binding:"required"`
		Type string `json:"type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "text is required")
		return
	}

	node, err := s.nodeSvc.GetNode(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	author := c.GetHeader("X-Agent-ID")
	if author == "" {
		author = "api"
	}

	ann := model.Annotation{
		ID:        ulid.Make().String(),
		Text:      req.Text,
		Author:    author,
		CreatedAt: s.clock(),
	}
	annotations := make([]model.Annotation, 0, len(node.Annotations)+1)
	annotations = append(annotations, node.Annotations...)
	annotations = append(annotations, ann)
	if setErr := s.store.SetAnnotations(c.Request.Context(), nodeID, annotations); setErr != nil {
		HandleError(c, setErr)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": nodeID, "annotation": ann})
}
