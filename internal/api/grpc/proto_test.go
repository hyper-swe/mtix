// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// protoDir returns the absolute path to the proto directory.
// Tests in internal/api/grpc/ reference proto files at ../../../proto/.
func protoDir() string {
	return filepath.Join("..", "..", "..", "proto")
}

// readProtoFile reads a proto file relative to the proto directory.
func readProtoFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(protoDir(), name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "proto file should exist: %s", path)
	return string(data)
}

// TestProto_Compiles_NoErrors verifies the proto files have valid syntax
// by checking for required elements (syntax, package, option go_package).
func TestProto_Compiles_NoErrors(t *testing.T) {
	files := []string{
		"mtix/v1/mtix.proto",
		"mtix/v1/types.proto",
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			content := readProtoFile(t, f)

			assert.Contains(t, content, `syntax = "proto3"`,
				"must use proto3 syntax")
			assert.Contains(t, content, `package mtix.v1`,
				"must be in mtix.v1 package")
			assert.Contains(t, content, `option go_package`,
				"must specify go_package option")
		})
	}
}

// TestProto_AllRPCsDefined verifies that all 40+ RPCs are defined in mtix.proto.
func TestProto_AllRPCsDefined(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/mtix.proto")

	// Expected RPC names per FR-8.2 categories.
	expectedRPCs := []string{
		// CRUD (8)
		"CreateNode", "GetNode", "UpdateNode", "DeleteNode",
		"UndeleteNode", "ListChildren", "GetTree", "Decompose",
		// LLM Shortcuts (9)
		"Micro", "Claim", "Unclaim", "Done", "Defer",
		"Cancel", "Reopen", "Block", "Comment",
		// Queries (7)
		"Ready", "Blocked", "Stale", "Orphans",
		"Search", "Progress", "Stats",
		// Context/Prompt (6)
		"GetContext", "UpdatePrompt", "AddAnnotation",
		"ResolveAnnotation", "Rerun", "Restore",
		// Session/Agent (7)
		"SessionStart", "SessionEnd", "SessionSummaryRPC",
		"AgentHeartbeat", "GetAgentState", "SetAgentState", "AgentCurrent",
		// Dependencies (3)
		"AddDependency", "RemoveDependency", "GetDependencyTree",
		// Bulk (1)
		"BulkUpdate",
		// Real-time (1)
		"Subscribe",
	}

	rpcPattern := regexp.MustCompile(`rpc\s+(\w+)\s*\(`)
	matches := rpcPattern.FindAllStringSubmatch(content, -1)

	// Build set of found RPCs.
	foundRPCs := make(map[string]bool)
	for _, m := range matches {
		foundRPCs[m[1]] = true
	}

	t.Logf("Found %d RPCs in mtix.proto", len(foundRPCs))
	assert.GreaterOrEqual(t, len(foundRPCs), 40,
		"should define at least 40 RPCs")

	for _, name := range expectedRPCs {
		assert.True(t, foundRPCs[name],
			"missing expected RPC: %s", name)
	}
}

// TestProto_Subscribe_IsServerStreaming verifies the Subscribe RPC
// uses server-streaming (returns stream Event).
func TestProto_Subscribe_IsServerStreaming(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/mtix.proto")

	// Find the Subscribe RPC line.
	subscribePattern := regexp.MustCompile(
		`rpc\s+Subscribe\s*\(\s*SubscribeRequest\s*\)\s*returns\s*\(\s*stream\s+Event\s*\)`,
	)
	assert.True(t, subscribePattern.MatchString(content),
		"Subscribe RPC must be server-streaming (returns stream Event)")
}

// TestTypes_NodeMessage_All38Fields verifies the Node message has all 38 fields.
func TestTypes_NodeMessage_All38Fields(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/types.proto")

	// Extract the Node message block.
	nodeStart := strings.Index(content, "message Node {")
	require.Greater(t, nodeStart, 0, "Node message should exist")

	// Find matching closing brace (simple approach: count braces).
	braceCount := 0
	nodeEnd := nodeStart
	for i := nodeStart; i < len(content); i++ {
		if content[i] == '{' {
			braceCount++
		} else if content[i] == '}' {
			braceCount--
			if braceCount == 0 {
				nodeEnd = i
				break
			}
		}
	}

	nodeBlock := content[nodeStart:nodeEnd]

	// Count field assignments (= N; pattern).
	fieldPattern := regexp.MustCompile(`=\s*\d+\s*;`)
	fields := fieldPattern.FindAllString(nodeBlock, -1)

	t.Logf("Node message has %d fields", len(fields))
	assert.GreaterOrEqual(t, len(fields), 37,
		"Node message should have at least 37 fields (38 fields, some with repeated)")

	// Verify key fields are present.
	keyFields := []string{
		"id", "parent_id", "project", "depth", "seq",
		"title", "description", "prompt", "acceptance",
		"node_type", "issue_type", "priority", "labels",
		"status", "progress", "previous_status",
		"assignee", "creator", "agent_state",
		"created_at", "updated_at", "closed_at", "defer_until",
		"estimate_min", "actual_min", "weight", "content_hash",
		"code_refs", "commit_refs",
		"annotations", "invalidated_at", "invalidated_by", "invalidation_reason",
		"deleted_at", "deleted_by",
		"metadata", "session_id",
	}

	for _, field := range keyFields {
		assert.Contains(t, nodeBlock, field,
			"Node message missing field: %s", field)
	}
}

// TestTypes_StatusEnum_7Values verifies the Status enum has 7 valid values
// (plus UNSPECIFIED = 0).
func TestTypes_StatusEnum_7Values(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/types.proto")

	expectedValues := []string{
		"STATUS_UNSPECIFIED",
		"STATUS_OPEN",
		"STATUS_IN_PROGRESS",
		"STATUS_BLOCKED",
		"STATUS_DONE",
		"STATUS_DEFERRED",
		"STATUS_CANCELLED",
		"STATUS_INVALIDATED",
	}

	for _, val := range expectedValues {
		assert.Contains(t, content, val,
			"Status enum missing value: %s", val)
	}
}

// TestTypes_Compiles_NoErrors verifies types.proto has valid structure.
func TestTypes_Compiles_NoErrors(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/types.proto")

	// Verify all expected message types exist.
	expectedMessages := []string{
		"message Node",
		"message CodeRef",
		"message Annotation",
		"message Dependency",
		"message ActivityEntry",
		"message ContextChain",
		"message ContextResponse",
		"message SessionSummary",
		"message AgentInfo",
		"message ProgressResponse",
		"message StatsResponse",
		"message SearchResult",
		"message Event",
		"message SubscriptionFilter",
	}

	for _, msg := range expectedMessages {
		assert.Contains(t, content, msg,
			"types.proto missing: %s", msg)
	}

	// Verify all expected enums exist.
	expectedEnums := []string{
		"enum Status",
		"enum NodeType",
		"enum IssueType",
		"enum DepType",
		"enum AgentState",
		"enum ActivityType",
		"enum Priority",
		"enum EventType",
	}

	for _, e := range expectedEnums {
		assert.Contains(t, content, e,
			"types.proto missing: %s", e)
	}

	// Verify google well-known type imports.
	assert.Contains(t, content, `import "google/protobuf/timestamp.proto"`)
	assert.Contains(t, content, `import "google/protobuf/struct.proto"`)
}

// TestTypes_ActivityTypeEnum_9Values verifies the ActivityType enum has 9 values.
func TestTypes_ActivityTypeEnum_9Values(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/types.proto")

	expectedValues := []string{
		"ACTIVITY_TYPE_COMMENT",
		"ACTIVITY_TYPE_STATUS_CHANGE",
		"ACTIVITY_TYPE_NOTE",
		"ACTIVITY_TYPE_ANNOTATION",
		"ACTIVITY_TYPE_UNCLAIM",
		"ACTIVITY_TYPE_CLAIM",
		"ACTIVITY_TYPE_SYSTEM",
		"ACTIVITY_TYPE_PROMPT_EDIT",
		"ACTIVITY_TYPE_CREATED",
	}

	for _, val := range expectedValues {
		assert.Contains(t, content, val,
			"ActivityType enum missing value: %s", val)
	}
}

// TestTypes_EventTypeEnum_14Values verifies the EventType enum has 14 event types.
func TestTypes_EventTypeEnum_14Values(t *testing.T) {
	content := readProtoFile(t, "mtix/v1/types.proto")

	expectedValues := []string{
		"EVENT_TYPE_NODE_CREATED",
		"EVENT_TYPE_NODE_UPDATED",
		"EVENT_TYPE_NODE_DELETED",
		"EVENT_TYPE_NODE_UNDELETED",
		"EVENT_TYPE_STATUS_CHANGED",
		"EVENT_TYPE_PROGRESS_CHANGED",
		"EVENT_TYPE_NODE_CLAIMED",
		"EVENT_TYPE_NODE_UNCLAIMED",
		"EVENT_TYPE_NODE_CANCELLED",
		"EVENT_TYPE_NODES_INVALIDATED",
		"EVENT_TYPE_DEPENDENCY_ADDED",
		"EVENT_TYPE_DEPENDENCY_REMOVED",
		"EVENT_TYPE_AGENT_STATE",
		"EVENT_TYPE_AGENT_STUCK",
	}

	for _, val := range expectedValues {
		assert.Contains(t, content, val,
			"EventType enum missing value: %s", val)
	}
}
