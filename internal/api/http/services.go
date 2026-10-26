// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// NodeReader exposes node queries without mutation capabilities per FR-7.3.
type NodeReader interface {
	GetNode(context.Context, string) (*model.Node, error)
	GetActivity(context.Context, string, int, int) ([]model.ActivityEntry, error)
	GetDirectChildren(context.Context, string) ([]*model.Node, error)
	GetAncestorChain(context.Context, string) ([]*model.Node, error)
	GetSiblings(context.Context, string) ([]*model.Node, error)
	ListNodes(context.Context, service.NodeFilter, service.ListOptions) ([]*model.Node, int, error)
	DistinctProjects(context.Context) ([]service.ProjectInfo, error)
}

// NodeWriter exposes workflow mutations per FR-7.3/FR-10.4.
type NodeWriter interface {
	CreateNode(context.Context, *service.CreateNodeRequest) (*model.Node, error)
	ApplyUpdate(context.Context, string, *service.NodeUpdate) error
	DeleteNode(context.Context, string, bool, string) error
	TransitionStatus(context.Context, string, model.Status, string, string) error
	DeferNode(context.Context, string, *time.Time, string, string) error
	ClaimNode(context.Context, string, string) error
	ForceReclaimNode(context.Context, string, string, time.Duration) error
	UnclaimNode(context.Context, string, string, string) error
	CancelNode(context.Context, string, string, string, bool) error
	AppendAnnotation(context.Context, string, model.Annotation) error
}

// DependencyReader exposes dependency queries per FR-4.2.
type DependencyReader interface {
	GetBlockers(context.Context, string) ([]*model.Dependency, error)
}

// DependencyWriter exposes dependency mutations per FR-4.
type DependencyWriter interface {
	AddDependency(context.Context, *model.Dependency) error
	RemoveDependency(context.Context, string, string, model.DepType) error
}

// AdminReader exposes integrity diagnostics per FR-6.3.
type AdminReader interface {
	Verify(context.Context) (*service.IntegrityReport, error)
}

// AdminWriter exposes backup and owned lifecycle operations per FR-6.3a/FR-7.1.
type AdminWriter interface {
	Backup(context.Context, string) (*service.BackupResult, error)
	Close() error
}

// BackgroundReader exposes ready-work queries per FR-7.3.
type BackgroundReader interface {
	GetReadyNodes(context.Context) ([]*model.Node, error)
}

// BackgroundWriter exposes retention scans per FR-6.3.
type BackgroundWriter interface{ RunScan(context.Context) error }

// SessionReader exposes summaries per FR-10.5.
type SessionReader interface {
	SessionSummary(context.Context, string) (*service.SessionSummary, error)
}

// SessionWriter exposes lifecycle per FR-10.5.
type SessionWriter interface {
	SessionStart(context.Context, string, string) (string, error)
	SessionEnd(context.Context, string) error
}

// AgentReader exposes queries per FR-10.3.
type AgentReader interface {
	GetAgentState(context.Context, string) (model.AgentState, error)
	GetLastHeartbeat(context.Context, string) (time.Time, error)
	GetCurrentWork(context.Context, string) (*model.Node, error)
	GetStaleAgents(context.Context, time.Duration) ([]string, error)
}

// AgentWriter exposes changes per FR-10.3.
type AgentWriter interface {
	Heartbeat(context.Context, string) error
	UpdateAgentState(context.Context, string, model.AgentState) error
}

// ConfigReader exposes configuration reads per FR-11.1.
type ConfigReader interface {
	Get(string) (string, error)
	AutoClaim() bool
	MaxRecommendedDepth() int
	AgentStaleThreshold() time.Duration
	SessionTimeout() time.Duration
}

// ConfigWriter exposes changes per FR-11.1.
type ConfigWriter interface {
	Set(string, string) (string, error)
}

// ReadServices supplies queries without requiring any writer per MTIX-99.1.
type ReadServices struct {
	Nodes        NodeReader
	Dependencies DependencyReader
	Admin        AdminReader
	Background   BackgroundReader
	Sessions     SessionReader
	Agents       AgentReader
	Config       ConfigReader
}

// WriteServices supplies mutations independently of read capabilities.
type WriteServices struct {
	Nodes        NodeWriter
	Dependencies DependencyWriter
	Admin        AdminWriter
	Background   BackgroundWriter
	Sessions     SessionWriter
	Agents       AgentWriter
	Config       ConfigWriter
}

// Services supplies the complete server boundary per FR-7.1.
type Services struct {
	Read  ReadServices
	Write WriteServices
}
