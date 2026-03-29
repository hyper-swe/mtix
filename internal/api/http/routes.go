// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"github.com/gin-gonic/gin"
)

// registerNodeRoutes mounts node CRUD endpoints per FR-7.2.
func (s *Server) registerNodeRoutes(rg *gin.RouterGroup) {
	nodes := rg.Group("/nodes")
	nodes.POST("", s.createNode)
	nodes.GET("/:id", s.getNode)
	nodes.PATCH("/:id", s.updateNode)
	nodes.DELETE("/:id", s.deleteNode)
	nodes.GET("/:id/children", s.getChildren)
	nodes.POST("/:id/decompose", s.decomposeNode)
	nodes.GET("/:id/activity", s.getActivity)
	nodes.GET("/:id/ancestors", s.nodeAncestors)
	nodes.GET("/:id/tree", s.nodeTree)
}

// registerBulkRoutes mounts bulk operation endpoints per FR-7.3.
func (s *Server) registerBulkRoutes(rg *gin.RouterGroup) {
	rg.PATCH("/bulk/nodes", s.bulkUpdateNodes)
}

// registerWorkflowRoutes mounts workflow transition endpoints per FR-7.2.
func (s *Server) registerWorkflowRoutes(rg *gin.RouterGroup) {
	wf := rg.Group("/nodes/:id")
	wf.POST("/claim", s.claimNode)
	wf.POST("/unclaim", s.unclaimNode)
	wf.POST("/done", s.doneNode)
	wf.POST("/defer", s.deferNode)
	wf.POST("/cancel", s.cancelNode)
	wf.POST("/reopen", s.reopenNode)
	wf.POST("/rerun", s.rerunNode)
	wf.POST("/block", s.blockNode)
	wf.POST("/comment", s.commentNode)
}

// registerQueryRoutes mounts query and search endpoints per FR-7.2.
func (s *Server) registerQueryRoutes(rg *gin.RouterGroup) {
	rg.GET("/search", s.searchNodes)
	rg.GET("/ready", s.readyNodes)
	rg.GET("/blocked", s.blockedNodes)
	rg.GET("/stale", s.staleNodes)
	rg.GET("/orphans", s.orphanNodes)
	rg.GET("/stats", s.projectStats)
	rg.GET("/progress/:id", s.nodeProgress)
	rg.GET("/tree/:id", s.nodeTree)
	rg.GET("/context/:id", s.nodeContext)
}

// registerDepRoutes mounts dependency management endpoints per FR-7.2.
func (s *Server) registerDepRoutes(rg *gin.RouterGroup) {
	deps := rg.Group("/deps")
	deps.POST("", s.addDependency)
	deps.DELETE("/:from/:to", s.removeDependency)
	deps.GET("/:id", s.getDependencies)
}

// registerAgentRoutes mounts agent and session endpoints per FR-7.2.
func (s *Server) registerAgentRoutes(rg *gin.RouterGroup) {
	agents := rg.Group("/agents")
	agents.POST("/:id/sessions/start", s.startSession)
	agents.POST("/:id/sessions/end", s.endSession)
	agents.GET("/:id/sessions/summary", s.sessionSummary)
	agents.POST("/:id/heartbeat", s.agentHeartbeat)
	agents.GET("/:id/state", s.getAgentState)
	agents.PATCH("/:id/state", s.setAgentState)
	agents.GET("/:id/work", s.getAgentWork)
}

// registerAdminRoutes mounts admin endpoints per FR-7.2.
func (s *Server) registerAdminRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin")
	admin.GET("/config", s.getConfig)
	admin.PATCH("/config", s.setConfig)
	admin.POST("/gc", s.runGC)
	admin.POST("/verify", s.runVerify)
	admin.POST("/backup", s.runBackup)
}
