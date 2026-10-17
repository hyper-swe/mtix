// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import "github.com/hyper-swe/mtix/internal/service"

// ToolSet holds the services every MCP tool category is built on, plus the
// scoping values the server passes to them. A zero ToolSet registers the
// same tool names with nil services, which is what the documentation guard
// uses to learn the registered names without a project (MTIX-107.42).
type ToolSet struct {
	Node       *service.NodeService
	Background *service.BackgroundService
	Context    *service.ContextService
	Prompt     *service.PromptService
	Dependency *service.DependencyService
	Inbox      *service.InboxService
	Session    *service.SessionService
	Agent      *service.AgentService
	Config     *service.ConfigService
	Sync       SyncWorkflowService
	// MtixDir is the project directory the sync workflow tool reads.
	MtixDir string
	// Primary is the default project prefix for omitted-project queries
	// and creates (MTIX-37.5).
	Primary string
	// Author is the author identity workflow tools record; empty keeps
	// their "mcp" default (MTIX-95.22).
	Author string
}

// RegisterAllTools registers every MCP tool category on reg. It is the one
// registration path: the mtix mcp command and the documentation tool-name
// guard both call it, so the guard can never disagree with the server about
// which tools exist (MTIX-107.42).
func RegisterAllTools(reg *ToolRegistry, s ToolSet) {
	RegisterNodeTools(reg, s.Node, WithPrimaryProject(s.Primary))
	RegisterWorkflowTools(reg, s.Node, s.Background,
		WithPrimaryProject(s.Primary), WithAuthor(s.Author))
	RegisterContextTools(reg, s.Context, s.Prompt)
	RegisterDepTools(reg, s.Dependency)
	RegisterInboxTools(reg, s.Inbox)
	RegisterSessionTools(reg, s.Session, s.Agent)
	RegisterAnalyticsTools(reg, s.Node, s.Agent, s.Config)
	RegisterDocsTools(reg)
	RegisterSyncWorkflowTool(reg, s.Sync, s.MtixDir)
}
