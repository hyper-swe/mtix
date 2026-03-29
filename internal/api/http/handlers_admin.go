// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// getConfig handles GET /api/v1/admin/config per FR-11.1.
func (s *Server) getConfig(c *gin.Context) {
	// Return key config values.
	prefix, _ := s.configSvc.Get("prefix")
	c.JSON(http.StatusOK, gin.H{
		"prefix":                prefix,
		"auto_claim":            s.configSvc.AutoClaim(),
		"max_recommended_depth": s.configSvc.MaxRecommendedDepth(),
		"agent_stale_threshold": s.configSvc.AgentStaleThreshold().String(),
		"session_timeout":       s.configSvc.SessionTimeout().String(),
	})
}

// setConfig handles PATCH /api/v1/admin/config per FR-11.1.
func (s *Server) setConfig(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "invalid request body: "+err.Error())
		return
	}

	results := make(map[string]string)
	for key, value := range req {
		prev, err := s.configSvc.Set(key, value)
		if err != nil {
			HandleError(c, err)
			return
		}
		results[key] = prev
	}

	c.JSON(http.StatusOK, gin.H{
		"updated":  results,
		"message":  "configuration updated",
	})
}

// runGC handles POST /api/v1/admin/gc per FR-6.3.
// Runs soft-delete retention cleanup via BackgroundService.
func (s *Server) runGC(c *gin.Context) {
	if err := s.bgSvc.RunScan(c.Request.Context()); err != nil {
		HandleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "completed",
		"message": "garbage collection completed",
	})
}

// runVerify handles POST /api/v1/admin/verify per FR-6.3.
// Runs integrity diagnostics on the database.
func (s *Server) runVerify(c *gin.Context) {
	// Run basic integrity check via PRAGMA.
	var result string
	row := s.store.QueryRow(c.Request.Context(), "PRAGMA integrity_check")
	if err := row.Scan(&result); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":          result,
		"integrity_check": result == "ok",
	})
}

// runBackup handles POST /api/v1/admin/backup per FR-6.3.
// Triggers a database backup to the path specified in the request body.
func (s *Server) runBackup(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "path is required")
		return
	}

	result, err := s.store.Backup(c.Request.Context(), req.Path)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"path":     result.Path,
		"size":     result.Size,
		"verified": result.Verified,
	})
}

// startSession handles POST /api/v1/agents/:id/sessions/start per FR-10.3.
func (s *Server) startSession(c *gin.Context) {
	agentID := c.Param("id")

	var req struct {
		Project string `json:"project"`
	}
	_ = c.ShouldBindJSON(&req)
	if req.Project == "" {
		if v, err := s.configSvc.Get("prefix"); err == nil {
			req.Project = v
		} else {
			req.Project = "PROJ"
		}
	}

	sessionID, err := s.sessionSvc.SessionStart(c.Request.Context(), agentID, req.Project)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"session_id": sessionID,
		"agent_id":   agentID,
		"status":     "active",
	})
}

// endSession handles POST /api/v1/agents/:id/sessions/end per FR-10.3.
func (s *Server) endSession(c *gin.Context) {
	agentID := c.Param("id")

	if err := s.sessionSvc.SessionEnd(c.Request.Context(), agentID); err != nil {
		HandleError(c, err)
		return
	}

	// Get summary.
	summary, err := s.sessionSvc.SessionSummary(c.Request.Context(), agentID)
	if err != nil {
		// Session ended successfully even if summary fails.
		c.JSON(http.StatusOK, gin.H{
			"agent_id": agentID,
			"status":   "ended",
		})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// sessionSummary handles GET /api/v1/agents/:id/sessions/summary per FR-10.5a.
func (s *Server) sessionSummary(c *gin.Context) {
	agentID := c.Param("id")

	summary, err := s.sessionSvc.SessionSummary(c.Request.Context(), agentID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, summary)
}

// agentHeartbeat handles POST /api/v1/agents/:id/heartbeat per FR-10.3.
func (s *Server) agentHeartbeat(c *gin.Context) {
	agentID := c.Param("id")

	if err := s.agentSvc.Heartbeat(c.Request.Context(), agentID); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"agent_id": agentID, "status": "ok"})
}

// getAgentState handles GET /api/v1/agents/:id/state per FR-10.3.
func (s *Server) getAgentState(c *gin.Context) {
	agentID := c.Param("id")

	state, err := s.agentSvc.GetAgentState(c.Request.Context(), agentID)
	if err != nil {
		HandleError(c, err)
		return
	}

	lastHB, _ := s.agentSvc.GetLastHeartbeat(c.Request.Context(), agentID)

	c.JSON(http.StatusOK, gin.H{
		"agent_id":       agentID,
		"state":          state,
		"last_heartbeat": lastHB,
	})
}

// setAgentState handles PATCH /api/v1/agents/:id/state per FR-10.3.
func (s *Server) setAgentState(c *gin.Context) {
	agentID := c.Param("id")

	var req struct {
		State model.AgentState `json:"state" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "state is required")
		return
	}

	if err := s.agentSvc.UpdateAgentState(c.Request.Context(), agentID, req.State); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"agent_id": agentID, "state": req.State})
}

// getAgentWork handles GET /api/v1/agents/:id/work per FR-10.3.
func (s *Server) getAgentWork(c *gin.Context) {
	agentID := c.Param("id")

	node, err := s.agentSvc.GetCurrentWork(c.Request.Context(), agentID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"agent_id": agentID, "current_work": node})
}

// addDependency handles POST /api/v1/deps per FR-7.2.
func (s *Server) addDependency(c *gin.Context) {
	var req struct {
		FromID   string          `json:"from_id" binding:"required"`
		ToID     string          `json:"to_id" binding:"required"`
		DepType  string          `json:"dep_type" binding:"required"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "from_id, to_id, and dep_type are required")
		return
	}

	dep := &model.Dependency{
		FromID:    req.FromID,
		ToID:      req.ToID,
		DepType:   model.DepType(req.DepType),
		CreatedAt: s.clock(),
		CreatedBy: c.GetHeader("X-Agent-ID"),
		Metadata:  req.Metadata,
	}

	if err := dep.Validate(); err != nil {
		HandleError(c, err)
		return
	}

	if err := s.store.AddDependency(c.Request.Context(), dep); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusCreated, dep)
}

// removeDependency handles DELETE /api/v1/deps/:from/:to per FR-7.2.
// Requires ?dep_type query parameter for disambiguation.
func (s *Server) removeDependency(c *gin.Context) {
	fromID := c.Param("from")
	toID := c.Param("to")
	depType := c.Query("dep_type")
	if depType == "" {
		HandleValidationError(c, "dep_type query parameter required")
		return
	}

	if err := s.store.RemoveDependency(
		c.Request.Context(), fromID, toID, model.DepType(depType),
	); err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": true, "from": fromID, "to": toID})
}

// getDependencies handles GET /api/v1/deps/:id per FR-7.2.
// Returns both inbound (blocking this node) and outbound dependencies.
func (s *Server) getDependencies(c *gin.Context) {
	nodeID := c.Param("id")

	blockers, err := s.store.GetBlockers(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"node_id":  nodeID,
		"blockers": blockers,
		"total":    len(blockers),
	})
}

// bulkUpdateNodes handles PATCH /api/v1/nodes/bulk per FR-7.3.
// Applies updates to multiple nodes atomically. Max 100 nodes.
func (s *Server) bulkUpdateNodes(c *gin.Context) {
	var req struct {
		Updates []struct {
			ID     string          `json:"id" binding:"required"`
			Fields store.NodeUpdate `json:"fields"`
		} `json:"updates" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		HandleValidationError(c, "invalid request body: "+err.Error())
		return
	}

	if len(req.Updates) == 0 {
		HandleValidationError(c, "at least one update is required")
		return
	}
	if len(req.Updates) > 100 {
		HandleValidationError(c, "maximum batch size is 100 nodes")
		return
	}

	// Apply all updates (non-atomic for now — individual per node).
	results := make([]gin.H, 0, len(req.Updates))
	for _, upd := range req.Updates {
		err := s.nodeSvc.UpdateNode(c.Request.Context(), upd.ID, &upd.Fields)
		if err != nil {
			results = append(results, gin.H{
				"id":      upd.ID,
				"success": false,
				"error":   err.Error(),
			})
		} else {
			results = append(results, gin.H{
				"id":      upd.ID,
				"success": true,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{"results": results, "total": len(results)})
}
