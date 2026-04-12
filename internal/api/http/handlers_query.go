// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// maxListLimit is the maximum allowed limit for pagination per FR-7.6.
const maxListLimit = 500

// csvQueryParam parses a query parameter as a multi-value list per FR-17.1.
// Accepts both comma-separated form (?key=a,b,c) and repeated form
// (?key=a&key=b&key=c). Returns nil for missing/empty input. Trims
// whitespace and skips empty elements.
func csvQueryParam(c *gin.Context, key string) []string {
	values := c.QueryArray(key)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// searchNodes handles GET /api/v1/search per FR-7.2, FR-7.6, FR-17.1.
// Multi-value filters accept either comma-separated (?under=A,B) or
// repeated query params (?under=A&under=B) — both forms produce slices.
func (s *Server) searchNodes(c *gin.Context) {
	filter := store.NodeFilter{
		Under:    csvQueryParam(c, "under"),
		Assignee: csvQueryParam(c, "assignee"),
		NodeType: csvQueryParam(c, "type"),
	}
	for _, st := range csvQueryParam(c, "status") {
		filter.Status = append(filter.Status, model.Status(st))
	}

	limit := clampLimit(parseIntParam(c, "limit", 50))
	offset := parseIntParam(c, "offset", 0)

	nodes, total, err := s.store.ListNodes(c.Request.Context(), filter, store.ListOptions{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"nodes":    nodes,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(nodes) < total,
	})
}

// readyNodes handles GET /api/v1/ready per FR-7.2.
func (s *Server) readyNodes(c *gin.Context) {
	nodes, err := s.bgSvc.GetReadyNodes(c.Request.Context())
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"nodes":    nodes,
		"total":    len(nodes),
		"has_more": false,
	})
}

// blockedNodes handles GET /api/v1/blocked per FR-7.2.
func (s *Server) blockedNodes(c *gin.Context) {
	limit := clampLimit(parseIntParam(c, "limit", 50))
	offset := parseIntParam(c, "offset", 0)

	filter := store.NodeFilter{Status: []model.Status{model.StatusBlocked}}
	nodes, total, err := s.store.ListNodes(c.Request.Context(), filter, store.ListOptions{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"nodes":    nodes,
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(nodes) < total,
	})
}

// staleNodes handles GET /api/v1/stale per FR-7.2.
// Supports ?hours=N to override the default stale threshold.
func (s *Server) staleNodes(c *gin.Context) {
	threshold := s.configSvc.AgentStaleThreshold()
	if hours := c.Query("hours"); hours != "" {
		if h, err := strconv.Atoi(hours); err == nil && h > 0 {
			threshold = time.Duration(h) * time.Hour
		}
	}

	agents, err := s.agentSvc.GetStaleAgents(c.Request.Context(), threshold)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"agents": agents, "total": len(agents)})
}

// orphanNodes handles GET /api/v1/orphans per FR-7.2.
// Returns root-level nodes (those with no parent).
// Fetches all nodes to filter roots in memory, then paginates the result.
// Uses a high limit because Limit=0 returns count-only in the store layer.
func (s *Server) orphanNodes(c *gin.Context) {
	limit := clampLimit(parseIntParam(c, "limit", 50))
	offset := parseIntParam(c, "offset", 0)

	// Fetch all nodes to ensure we find every root regardless of child count.
	nodes, _, err := s.store.ListNodes(c.Request.Context(), store.NodeFilter{}, store.ListOptions{
		Limit:  100000,
		Offset: 0,
	})
	if err != nil {
		HandleError(c, err)
		return
	}

	var roots []*model.Node
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, n)
		}
	}

	// Apply offset/limit on filtered results.
	start := offset
	if start > len(roots) {
		start = len(roots)
	}
	end := start + limit
	if end > len(roots) {
		end = len(roots)
	}
	page := roots[start:end]

	c.JSON(http.StatusOK, gin.H{
		"nodes":    page,
		"total":    len(roots),
		"limit":    limit,
		"offset":   offset,
		"has_more": end < len(roots),
	})
}

// projectStats handles GET /api/v1/stats per FR-7.2.
// Returns project-wide statistics by status.
func (s *Server) projectStats(c *gin.Context) {
	// Count nodes by status.
	statuses := []model.Status{
		model.StatusOpen, model.StatusInProgress, model.StatusDone,
		model.StatusBlocked, model.StatusDeferred, model.StatusCancelled,
	}

	counts := make(map[string]int)
	totalNodes := 0
	for _, st := range statuses {
		_, count, err := s.store.ListNodes(c.Request.Context(), store.NodeFilter{
			Status: []model.Status{st},
		}, store.ListOptions{Limit: 0})
		if err != nil {
			HandleError(c, err)
			return
		}
		counts[string(st)] = count
		totalNodes += count
	}

	c.JSON(http.StatusOK, gin.H{
		"total":  totalNodes,
		"counts": counts,
	})
}

// nodeProgress handles GET /api/v1/progress/:id per FR-7.2 and FR-5.6a.
func (s *Server) nodeProgress(c *gin.Context) {
	nodeID := c.Param("id")
	node, err := s.nodeSvc.GetNode(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	// Count invalidated children for FR-5.6a.
	children, childErr := s.store.GetDirectChildren(c.Request.Context(), nodeID)
	invalidatedCount := 0
	if childErr == nil {
		for _, ch := range children {
			if ch.InvalidatedAt != nil {
				invalidatedCount++
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"id":                nodeID,
		"progress":          node.Progress,
		"status":            node.Status,
		"invalidated_count": invalidatedCount,
	})
}

// nodeTree handles GET /api/v1/tree/:id and /api/v1/nodes/:id/tree per FR-7.2.
// Returns the subtree rooted at the given node. Supports ?depth param.
func (s *Server) nodeTree(c *gin.Context) {
	nodeID := c.Param("id")
	maxDepth := parseIntParam(c, "depth", 10)

	root, err := s.nodeSvc.GetNode(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	tree := s.buildTree(c, root, 0, maxDepth)
	c.JSON(http.StatusOK, tree)
}

// buildTree recursively builds a tree structure for JSON response.
func (s *Server) buildTree(c *gin.Context, node *model.Node, depth, maxDepth int) gin.H {
	result := gin.H{
		"id":       node.ID,
		"title":    node.Title,
		"status":   node.Status,
		"progress": node.Progress,
		"depth":    depth,
	}

	if depth >= maxDepth {
		return result
	}

	children, err := s.store.GetDirectChildren(c.Request.Context(), node.ID)
	if err != nil || len(children) == 0 {
		result["children"] = []gin.H{}
		return result
	}

	childTrees := make([]gin.H, 0, len(children))
	for _, ch := range children {
		childTrees = append(childTrees, s.buildTree(c, ch, depth+1, maxDepth))
	}
	result["children"] = childTrees
	return result
}

// nodeContext handles GET /api/v1/context/:id per FR-12.6.
// Returns assembled context for a node including ancestors, siblings, etc.
func (s *Server) nodeContext(c *gin.Context) {
	nodeID := c.Param("id")

	node, err := s.nodeSvc.GetNode(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	ancestors, _ := s.store.GetAncestorChain(c.Request.Context(), nodeID)
	siblings, _ := s.store.GetSiblings(c.Request.Context(), nodeID)
	children, _ := s.store.GetDirectChildren(c.Request.Context(), nodeID)

	c.JSON(http.StatusOK, gin.H{
		"node":      node,
		"ancestors": ancestors,
		"siblings":  siblings,
		"children":  children,
	})
}

// nodeAncestors handles GET /api/v1/nodes/:id/ancestors per FR-7.2.
// Returns the breadcrumb path from root to the specified node.
func (s *Server) nodeAncestors(c *gin.Context) {
	nodeID := c.Param("id")

	ancestors, err := s.store.GetAncestorChain(c.Request.Context(), nodeID)
	if err != nil {
		HandleError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ancestors": ancestors,
		"total":     len(ancestors),
	})
}

// parseIntParam parses an integer query parameter with a default value.
func parseIntParam(c *gin.Context, name string, defaultVal int) int {
	raw := c.Query(name)
	if raw == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return defaultVal
	}
	return v
}

// clampLimit enforces the maximum list limit per FR-7.6.
func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}
