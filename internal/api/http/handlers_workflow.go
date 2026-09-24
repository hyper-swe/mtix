// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/oklog/ulid/v2"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
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

// deferRequestBody is the optional JSON body of POST /api/v1/nodes/:id/defer.
type deferRequestBody struct {
	Until string `json:"until"`
}

// decodeDeferBody reads the optional defer body (MTIX-95.22). An empty or
// blank body means no until. Anything else must be exactly one JSON object
// whose only key is until: a literal null, an unknown key, a value of the
// wrong type or data after the object is an error.
func decodeDeferBody(body io.Reader) (string, error) {
	if body == nil {
		return "", nil
	}
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req *deferRequestBody
	if err := dec.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", fmt.Errorf("decode defer body: %w", err)
	}
	if req == nil {
		return "", errors.New("defer body is null")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("defer body has data after the JSON object")
	}
	return req.Until, nil
}

// deferNode handles POST /api/v1/nodes/:id/defer per FR-3.8 and FR-3.8b.
// Accepts an optional {"until": "<RFC 3339>"} body; an empty body defers with
// no wake time. A body decodeDeferBody rejects, or an until that is not an
// RFC 3339 timestamp, is answered with 400 before anything changes. The wake
// time is stored with the transition by NodeService.DeferNode (MTIX-95.22).
// The author is the X-Agent-ID header, "api" without one.
func (s *Server) deferNode(c *gin.Context) {
	nodeID := c.Param("id")

	until, err := decodeDeferBody(c.Request.Body)
	if err != nil {
		HandleValidationError(c, "request body must be empty or a JSON object whose only key is until, such as {\"until\":\"2026-04-01T00:00:00Z\"}")
		return
	}

	wake, err := service.ParseDeferUntil(until)
	if err != nil {
		HandleValidationError(c, "invalid until timestamp: must be RFC 3339 with a zone, such as 2026-04-01T00:00:00Z")
		return
	}

	agentID := c.GetHeader("X-Agent-ID")
	if agentID == "" {
		agentID = "api"
	}

	if err := s.nodeSvc.DeferNode(
		c.Request.Context(), nodeID, wake, "deferred via API", agentID,
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
		To   string `json:"to"`
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

	// An explicit `to` addresses the comment at an agent's inbox (FR-19.1);
	// otherwise an @<agent> token in the text sets the addressee.
	addressee := req.To
	if addressee == "" {
		addressee = service.ParseAddressee(req.Text)
	}

	ann := model.Annotation{
		ID:        ulid.Make().String(),
		Text:      req.Text,
		Author:    author,
		CreatedAt: s.clock(),
		Addressee: addressee,
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
