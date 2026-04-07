// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestNodeType_Constants verifies all node type constants are defined.
func TestNodeType_Constants(t *testing.T) {
	assert.Equal(t, model.NodeType("story"), model.NodeTypeStory)
	assert.Equal(t, model.NodeType("epic"), model.NodeTypeEpic)
	assert.Equal(t, model.NodeType("issue"), model.NodeTypeIssue)
	assert.Equal(t, model.NodeType("micro"), model.NodeTypeMicro)
	assert.Equal(t, model.NodeType("auto"), model.NodeTypeAuto)
}

// TestNodeTypeForDepth_IndustryConvention verifies depth-to-type mapping
// follows Agile/Scrum convention: Epic (depth 0) > Story (depth 1) > Issue > Micro.
func TestNodeTypeForDepth_IndustryConvention(t *testing.T) {
	assert.Equal(t, model.NodeTypeEpic, model.NodeTypeForDepth(0), "depth 0 should be epic")
	assert.Equal(t, model.NodeTypeStory, model.NodeTypeForDepth(1), "depth 1 should be story")
	assert.Equal(t, model.NodeTypeIssue, model.NodeTypeForDepth(2), "depth 2 should be issue")
	assert.Equal(t, model.NodeTypeMicro, model.NodeTypeForDepth(3), "depth 3 should be micro")
	assert.Equal(t, model.NodeTypeMicro, model.NodeTypeForDepth(10), "deep nodes should be micro")
}

// TestIssueType_Constants verifies all issue type constants are defined.
func TestIssueType_Constants(t *testing.T) {
	assert.Equal(t, model.IssueType("bug"), model.IssueTypeBug)
	assert.Equal(t, model.IssueType("feature"), model.IssueTypeFeature)
	assert.Equal(t, model.IssueType("task"), model.IssueTypeTask)
	assert.Equal(t, model.IssueType("chore"), model.IssueTypeChore)
	assert.Equal(t, model.IssueType("refactor"), model.IssueTypeRefactor)
	assert.Equal(t, model.IssueType("test"), model.IssueTypeTest)
	assert.Equal(t, model.IssueType("doc"), model.IssueTypeDoc)
}

// TestAgentState_Constants verifies all agent state constants are defined.
func TestAgentState_Constants(t *testing.T) {
	assert.Equal(t, model.AgentState("idle"), model.AgentStateIdle)
	assert.Equal(t, model.AgentState("working"), model.AgentStateWorking)
	assert.Equal(t, model.AgentState("stuck"), model.AgentStateStuck)
	assert.Equal(t, model.AgentState("done"), model.AgentStateDone)
}
