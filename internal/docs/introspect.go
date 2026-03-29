// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"sort"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// TemplateData holds all data needed to render documentation templates per FR-13.2.
type TemplateData struct {
	// ProjectPrefix is the project's prefix (e.g., "PROJ").
	ProjectPrefix string

	// Version is the mtix binary version.
	Version string

	// Commands is the CLI command tree extracted from Cobra.
	Commands []CommandInfo

	// Transitions is the state machine transitions.
	Transitions []model.TransitionInfo

	// Statuses is all defined node statuses.
	Statuses []model.Status

	// MCPTools is the list of MCP tool definitions.
	MCPTools []MCPToolInfo

	// ConfigKeys is the list of valid configuration keys.
	ConfigKeys []string

	// ErrorCodes is the list of sentinel error names.
	ErrorCodes []string
}

// CommandInfo describes a CLI command for documentation per FR-13.2.
type CommandInfo struct {
	Name        string        `json:"name"`
	Use         string        `json:"use"`
	Short       string        `json:"short"`
	Long        string        `json:"long,omitempty"`
	Flags       []FlagInfo    `json:"flags,omitempty"`
	SubCommands []CommandInfo `json:"sub_commands,omitempty"`
}

// FlagInfo describes a CLI flag.
type FlagInfo struct {
	Name         string `json:"name"`
	Shorthand    string `json:"shorthand,omitempty"`
	Usage        string `json:"usage"`
	DefaultValue string `json:"default_value,omitempty"`
	Required     bool   `json:"required,omitempty"`
}

// MCPToolInfo describes an MCP tool for documentation per FR-13.2.
type MCPToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// IntrospectCLI walks the Cobra command tree and extracts command info per FR-13.2.
// Returns a flat list of all commands including subcommands.
func IntrospectCLI(root *cobra.Command) []CommandInfo {
	var commands []CommandInfo
	walkCommand(root, &commands)
	return commands
}

// walkCommand recursively extracts command info from a Cobra command tree.
func walkCommand(cmd *cobra.Command, result *[]CommandInfo) {
	info := CommandInfo{
		Name:  cmd.Name(),
		Use:   cmd.Use,
		Short: cmd.Short,
		Long:  cmd.Long,
	}

	// Extract flags.
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		// Skip inherited flags to avoid duplication.
		if cmd.InheritedFlags().Lookup(f.Name) != nil {
			return
		}
		fi := FlagInfo{
			Name:         f.Name,
			Shorthand:    f.Shorthand,
			Usage:        f.Usage,
			DefaultValue: f.DefValue,
		}
		info.Flags = append(info.Flags, fi)
	})

	// Extract subcommands.
	for _, sub := range cmd.Commands() {
		subInfo := CommandInfo{
			Name:  sub.Name(),
			Use:   sub.Use,
			Short: sub.Short,
		}
		info.SubCommands = append(info.SubCommands, subInfo)
	}

	*result = append(*result, info)

	// Recurse into subcommands.
	for _, sub := range cmd.Commands() {
		walkCommand(sub, result)
	}
}

// IntrospectStateMachine extracts state machine transitions per FR-13.2.
func IntrospectStateMachine() []model.TransitionInfo {
	transitions := model.GetAllTransitions()

	// Sort for deterministic output.
	sort.Slice(transitions, func(i, j int) bool {
		if transitions[i].From != transitions[j].From {
			return transitions[i].From < transitions[j].From
		}
		return transitions[i].To < transitions[j].To
	})

	return transitions
}

// IntrospectMCPTools extracts MCP tool definitions per FR-13.2.
func IntrospectMCPTools(reg *mcp.ToolRegistry) []MCPToolInfo {
	tools := reg.List()
	result := make([]MCPToolInfo, 0, len(tools))
	for _, t := range tools {
		result = append(result, MCPToolInfo{
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return result
}

// IntrospectConfig extracts valid configuration keys per FR-13.2.
func IntrospectConfig() []string {
	return service.ValidConfigKeys()
}

// IntrospectErrors returns sentinel error names for troubleshooting docs.
func IntrospectErrors() []string {
	return []string{
		"ErrNotFound",
		"ErrAlreadyExists",
		"ErrInvalidInput",
		"ErrInvalidTransition",
		"ErrCycleDetected",
		"ErrConflict",
		"ErrAlreadyClaimed",
		"ErrNodeBlocked",
		"ErrStillDeferred",
		"ErrAgentStillActive",
		"ErrNoActiveSession",
		"ErrInvalidConfigKey",
		"ErrDepthWarning",
	}
}

// BuildTemplateData assembles all introspection data for template rendering.
func BuildTemplateData(
	root *cobra.Command,
	reg *mcp.ToolRegistry,
	prefix, version string,
) *TemplateData {
	return &TemplateData{
		ProjectPrefix: prefix,
		Version:       version,
		Commands:      IntrospectCLI(root),
		Transitions:   IntrospectStateMachine(),
		Statuses:      model.AllStatuses(),
		MCPTools:      IntrospectMCPTools(reg),
		ConfigKeys:    IntrospectConfig(),
		ErrorCodes:    IntrospectErrors(),
	}
}
