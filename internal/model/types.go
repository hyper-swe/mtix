// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

// NodeType represents the tier classification of a node.
// Tier labels are CANONICALLY DERIVED FROM DEPTH per FR-1.2:
//   - At node creation (NodeService.CreateNode): NodeType is set from NodeTypeForDepth(depth).
//   - At import (sqlite.Import / insertExportNode / updateExportNode): NodeType is overridden
//     with NodeTypeForDepth(depth), ignoring any value in the input file (tamper resistance).
//   - At export (sqlite.exportNodes): NodeType is overridden with NodeTypeForDepth(depth),
//     normalizing any legacy stored value so export -> import -> export is byte-idempotent.
//
// In practice, callers should never need to set NodeType manually — the canonical value
// is whatever NodeTypeForDepth says for that depth.
type NodeType string

const (
	// NodeTypeEpic represents a top-level initiative (depth 0).
	NodeTypeEpic NodeType = "epic"

	// NodeTypeStory represents a user story or feature (depth 1).
	NodeTypeStory NodeType = "story"

	// NodeTypeIssue represents a concrete work item (depth 2).
	NodeTypeIssue NodeType = "issue"

	// NodeTypeMicro represents a micro work item (depth 3+).
	NodeTypeMicro NodeType = "micro"

	// NodeTypeAuto indicates the type should be derived from depth.
	NodeTypeAuto NodeType = "auto"
)

// NodeTypeForDepth derives the node type from hierarchy depth per FR-1.2.
// Follows Agile/Scrum convention: depth 0 → epic, depth 1 → story, depth 2 → issue, depth 3+ → micro.
func NodeTypeForDepth(depth int) NodeType {
	switch depth {
	case 0:
		return NodeTypeEpic
	case 1:
		return NodeTypeStory
	case 2:
		return NodeTypeIssue
	default:
		return NodeTypeMicro
	}
}

// IssueType classifies the nature of the work.
type IssueType string

const (
	// IssueTypeBug represents a defect fix.
	IssueTypeBug IssueType = "bug"

	// IssueTypeFeature represents new functionality.
	IssueTypeFeature IssueType = "feature"

	// IssueTypeTask represents general work.
	IssueTypeTask IssueType = "task"

	// IssueTypeChore represents maintenance work.
	IssueTypeChore IssueType = "chore"

	// IssueTypeRefactor represents code restructuring.
	IssueTypeRefactor IssueType = "refactor"

	// IssueTypeTest represents test creation or improvement.
	IssueTypeTest IssueType = "test"

	// IssueTypeDoc represents documentation work.
	IssueTypeDoc IssueType = "doc"
)

// AgentState represents the current state of an LLM agent.
type AgentState string

const (
	// AgentStateIdle indicates the agent is not working on anything.
	AgentStateIdle AgentState = "idle"

	// AgentStateWorking indicates the agent is actively working.
	AgentStateWorking AgentState = "working"

	// AgentStateStuck indicates the agent has encountered a problem.
	AgentStateStuck AgentState = "stuck"

	// AgentStateDone indicates the agent has completed its work.
	AgentStateDone AgentState = "done"
)

// Priority represents the importance of a node.
// Values are 1-indexed (1=critical through 5=backlog) per FR-3.1.
type Priority int

const (
	// PriorityCritical is the highest priority (1).
	PriorityCritical Priority = 1

	// PriorityHigh is high priority (2).
	PriorityHigh Priority = 2

	// PriorityMedium is the default priority (3).
	PriorityMedium Priority = 3

	// PriorityLow is low priority (4).
	PriorityLow Priority = 4

	// PriorityBacklog is the lowest priority (5).
	PriorityBacklog Priority = 5
)

// IsValid returns true if the priority is in the valid range [1, 5].
func (p Priority) IsValid() bool {
	return p >= PriorityCritical && p <= PriorityBacklog
}

// DepType represents the type of dependency relationship per FR-4.2.
type DepType string

const (
	// DepTypeBlocks indicates a hard blocker (A blocks B).
	DepTypeBlocks DepType = "blocks"

	// DepTypeRelated indicates a soft informational link.
	DepTypeRelated DepType = "related"

	// DepTypeDiscoveredFrom indicates a relationship found during work.
	DepTypeDiscoveredFrom DepType = "discovered_from"

	// DepTypeDuplicates indicates a duplicate of another node.
	DepTypeDuplicates DepType = "duplicates"
)

// IsValid returns true if the dependency type is recognized.
func (d DepType) IsValid() bool {
	switch d {
	case DepTypeBlocks, DepTypeRelated, DepTypeDiscoveredFrom, DepTypeDuplicates:
		return true
	default:
		return false
	}
}
