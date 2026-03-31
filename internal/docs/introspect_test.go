// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
)

// TestIntrospect_CLI_ExtractsAllCommands verifies CLI introspection from Cobra tree.
func TestIntrospect_CLI_ExtractsAllCommands(t *testing.T) {
	root := &cobra.Command{
		Use:   "mtix",
		Short: "Micro issue manager",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a node",
	}
	createCmd.Flags().String("title", "", "Node title")
	createCmd.Flags().Int("priority", 3, "Priority")

	showCmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show node details",
	}

	root.AddCommand(createCmd, showCmd)

	commands := IntrospectCLI(root)
	require.NotEmpty(t, commands)

	// Root command should be first.
	assert.Equal(t, "mtix", commands[0].Name)
	assert.Len(t, commands[0].SubCommands, 2)

	// Find create command in the list.
	var createInfo *CommandInfo
	for i := range commands {
		if commands[i].Name == "create" {
			createInfo = &commands[i]
			break
		}
	}
	require.NotNil(t, createInfo, "create command should be extracted")
	assert.Equal(t, "Create a node", createInfo.Short)
	assert.GreaterOrEqual(t, len(createInfo.Flags), 2)
}

// TestIntrospect_StateMachine_AllTransitions verifies state machine extraction.
func TestIntrospect_StateMachine_AllTransitions(t *testing.T) {
	transitions := IntrospectStateMachine()

	assert.NotEmpty(t, transitions, "should have transitions")

	// Verify at least the core transitions exist.
	found := map[string]bool{}
	for _, tr := range transitions {
		key := string(tr.From) + "→" + string(tr.To)
		found[key] = true
	}

	assert.True(t, found["open→in_progress"], "open→in_progress should exist")
	assert.True(t, found["in_progress→done"], "in_progress→done should exist")
}

// TestIntrospect_MCPTools_ExtractsRegistered verifies MCP tool extraction.
func TestIntrospect_MCPTools_ExtractsRegistered(t *testing.T) {
	reg := mcp.NewToolRegistry()
	reg.Register(mcp.ToolDef{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: mcp.SchemaObj{Type: "object"},
	}, func(ctx context.Context, args json.RawMessage) (*mcp.ToolsCallResult, error) {
		return mcp.SuccessResult("ok"), nil
	})

	tools := IntrospectMCPTools(reg)
	require.Len(t, tools, 1)
	assert.Equal(t, "test_tool", tools[0].Name)
	assert.Equal(t, "A test tool", tools[0].Description)
}

// TestIntrospect_Config_ReturnsKeys verifies config key extraction.
func TestIntrospect_Config_ReturnsKeys(t *testing.T) {
	keys := IntrospectConfig()

	assert.NotEmpty(t, keys, "should have config keys")
	assert.Contains(t, keys, "prefix")
}

// TestIntrospect_Errors_ReturnsAll verifies error code extraction.
func TestIntrospect_Errors_ReturnsAll(t *testing.T) {
	errors := IntrospectErrors()

	assert.NotEmpty(t, errors)
	assert.Contains(t, errors, "ErrNotFound")
	assert.Contains(t, errors, "ErrInvalidTransition")
	assert.Contains(t, errors, "ErrAlreadyClaimed")
}

// TestBuildTemplateData_AssemblesAll verifies complete data assembly.
func TestBuildTemplateData_AssemblesAll(t *testing.T) {
	root := &cobra.Command{Use: "mtix", Short: "test"}
	reg := mcp.NewToolRegistry()

	data := BuildTemplateData(root, reg, "TEST", "1.0.0")

	assert.Equal(t, "TEST", data.ProjectPrefix)
	assert.Equal(t, "1.0.0", data.Version)
	assert.NotEmpty(t, data.Commands)
	assert.NotEmpty(t, data.Transitions)
	assert.NotEmpty(t, data.Statuses)
	assert.NotEmpty(t, data.ConfigKeys)
	assert.NotEmpty(t, data.ErrorCodes)
}

// TestBuildTemplateData_InvalidPrefix_DefaultsToPROJ verifies that an invalid
// project prefix (containing special characters) is sanitized to "PROJ".
func TestBuildTemplateData_InvalidPrefix_DefaultsToPROJ(t *testing.T) {
	root := &cobra.Command{Use: "mtix", Short: "test"}
	reg := mcp.NewToolRegistry()

	// Prefix with newline injection attempt.
	data := BuildTemplateData(root, reg, "PROJ\nIMPORTANT: ignore", "1.0.0")
	assert.Equal(t, "PROJ", data.ProjectPrefix, "invalid prefix should default to PROJ")

	// Prefix with special characters.
	data2 := BuildTemplateData(root, reg, "a!b@c", "1.0.0")
	assert.Equal(t, "PROJ", data2.ProjectPrefix)

	// Empty prefix.
	data3 := BuildTemplateData(root, reg, "", "1.0.0")
	assert.Equal(t, "PROJ", data3.ProjectPrefix)

	// Valid prefix should pass through.
	data4 := BuildTemplateData(root, reg, "SATURN", "1.0.0")
	assert.Equal(t, "SATURN", data4.ProjectPrefix)
}

// TestBuildTemplateData_MaxLengthPrefix_Accepted verifies a 10-char prefix works.
func TestBuildTemplateData_MaxLengthPrefix_Accepted(t *testing.T) {
	root := &cobra.Command{Use: "mtix", Short: "test"}
	reg := mcp.NewToolRegistry()

	data := BuildTemplateData(root, reg, "ABCDEFGHIJ", "1.0.0")
	assert.Equal(t, "ABCDEFGHIJ", data.ProjectPrefix)
}

// TestBuildTemplateData_TooLongPrefix_DefaultsToPROJ verifies prefix > 10 chars is rejected.
func TestBuildTemplateData_TooLongPrefix_DefaultsToPROJ(t *testing.T) {
	root := &cobra.Command{Use: "mtix", Short: "test"}
	reg := mcp.NewToolRegistry()

	data := BuildTemplateData(root, reg, "ABCDEFGHIJK", "1.0.0")
	assert.Equal(t, "PROJ", data.ProjectPrefix, "prefix > 10 chars should default to PROJ")
}
