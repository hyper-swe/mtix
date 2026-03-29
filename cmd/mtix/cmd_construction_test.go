// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Command construction tests — verifies flags, args, and subcommands
// ============================================================================

// TestNewCreateCmd_HasExpectedFlags_AllRegistered verifies create command flags.
func TestNewCreateCmd_HasExpectedFlags_AllRegistered(t *testing.T) {
	cmd := newCreateCmd()

	require.NotNil(t, cmd)
	assert.Equal(t, "create <title>", cmd.Use)

	flags := []string{"under", "type", "priority", "description", "prompt", "acceptance", "labels", "assign"}
	for _, f := range flags {
		t.Run(f, func(t *testing.T) {
			assert.NotNil(t, cmd.Flags().Lookup(f), "flag --%s should exist", f)
		})
	}
}

// TestNewCreateCmd_PriorityDefault_IsThree verifies default priority.
func TestNewCreateCmd_PriorityDefault_IsThree(t *testing.T) {
	cmd := newCreateCmd()

	pFlag := cmd.Flags().Lookup("priority")
	require.NotNil(t, pFlag)
	assert.Equal(t, "3", pFlag.DefValue)
}

// TestNewCreateCmd_RequiresExactlyOneArg verifies arg count validation.
func TestNewCreateCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newCreateCmd()

	err := cmd.Args(cmd, []string{})
	assert.Error(t, err)

	err = cmd.Args(cmd, []string{"title"})
	assert.NoError(t, err)

	err = cmd.Args(cmd, []string{"a", "b"})
	assert.Error(t, err)
}

// TestNewMicroCmd_HasExpectedFlags_AllRegistered verifies micro command flags.
func TestNewMicroCmd_HasExpectedFlags_AllRegistered(t *testing.T) {
	cmd := newMicroCmd()

	require.NotNil(t, cmd)
	assert.Equal(t, "micro <title>", cmd.Use)

	flags := []string{"under", "prompt", "acceptance", "labels"}
	for _, f := range flags {
		t.Run(f, func(t *testing.T) {
			assert.NotNil(t, cmd.Flags().Lookup(f), "flag --%s should exist", f)
		})
	}
}

// TestNewMicroCmd_UnderIsRequired verifies --under is required.
func TestNewMicroCmd_UnderIsRequired(t *testing.T) {
	cmd := newMicroCmd()
	underFlag := cmd.Flags().Lookup("under")
	require.NotNil(t, underFlag)
	// cobra annotations for required flags
	annotations := underFlag.Annotations
	_, hasRequired := annotations["cobra_annotation_bash_completion_one_required_flag"]
	assert.True(t, hasRequired, "--under should be marked required")
}

// TestNewShowCmd_RequiresExactlyOneArg verifies show command args.
func TestNewShowCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newShowCmd()

	assert.Equal(t, "show <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b"}))
}

// TestNewListCmd_HasFilterFlags_AllRegistered verifies list command flags.
func TestNewListCmd_HasFilterFlags_AllRegistered(t *testing.T) {
	cmd := newListCmd()

	assert.Equal(t, "list", cmd.Use)
	assert.Equal(t, []string{"ls"}, cmd.Aliases)

	flags := []string{"status", "under", "assignee", "priority", "limit"}
	for _, f := range flags {
		t.Run(f, func(t *testing.T) {
			assert.NotNil(t, cmd.Flags().Lookup(f), "flag --%s should exist", f)
		})
	}
}

// TestNewListCmd_LimitDefault_Is50 verifies default limit.
func TestNewListCmd_LimitDefault_Is50(t *testing.T) {
	cmd := newListCmd()

	limitFlag := cmd.Flags().Lookup("limit")
	require.NotNil(t, limitFlag)
	assert.Equal(t, "50", limitFlag.DefValue)
}

// TestNewTreeCmd_HasDepthFlag verifies tree command depth flag.
func TestNewTreeCmd_HasDepthFlag(t *testing.T) {
	cmd := newTreeCmd()

	assert.Equal(t, "tree <id>", cmd.Use)
	depthFlag := cmd.Flags().Lookup("depth")
	require.NotNil(t, depthFlag)
	assert.Equal(t, "10", depthFlag.DefValue)
}

// TestNewTreeCmd_RequiresExactlyOneArg verifies tree command args.
func TestNewTreeCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newTreeCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}

// TestNewUpdateCmd_HasAllFieldFlags verifies update command flags.
func TestNewUpdateCmd_HasAllFieldFlags(t *testing.T) {
	cmd := newUpdateCmd()

	assert.Equal(t, "update <id>", cmd.Use)

	flags := []string{"title", "description", "prompt", "acceptance", "priority", "labels", "assignee"}
	for _, f := range flags {
		t.Run(f, func(t *testing.T) {
			assert.NotNil(t, cmd.Flags().Lookup(f), "flag --%s should exist", f)
		})
	}
}

// TestNewClaimCmd_HasAgentFlag_Required verifies claim command flags.
func TestNewClaimCmd_HasAgentFlag_Required(t *testing.T) {
	cmd := newClaimCmd()

	assert.Equal(t, "claim <id>", cmd.Use)
	agentFlag := cmd.Flags().Lookup("agent")
	require.NotNil(t, agentFlag)

	annotations := agentFlag.Annotations
	_, hasRequired := annotations["cobra_annotation_bash_completion_one_required_flag"]
	assert.True(t, hasRequired, "--agent should be marked required")
}

// TestNewUnclaimCmd_HasReasonFlag_Required verifies unclaim command flags.
func TestNewUnclaimCmd_HasReasonFlag_Required(t *testing.T) {
	cmd := newUnclaimCmd()

	assert.Equal(t, "unclaim <id>", cmd.Use)
	reasonFlag := cmd.Flags().Lookup("reason")
	require.NotNil(t, reasonFlag)

	annotations := reasonFlag.Annotations
	_, hasRequired := annotations["cobra_annotation_bash_completion_one_required_flag"]
	assert.True(t, hasRequired, "--reason should be marked required")
}

// TestNewDoneCmd_RequiresExactlyOneArg verifies done command args.
func TestNewDoneCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newDoneCmd()

	assert.Equal(t, "done <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}

// TestNewDeferCmd_HasUntilFlag verifies defer command flags.
func TestNewDeferCmd_HasUntilFlag(t *testing.T) {
	cmd := newDeferCmd()

	assert.Equal(t, "defer <id>", cmd.Use)
	untilFlag := cmd.Flags().Lookup("until")
	require.NotNil(t, untilFlag)
}

// TestNewCancelCmd_HasReasonAndCascadeFlags verifies cancel command flags.
func TestNewCancelCmd_HasReasonAndCascadeFlags(t *testing.T) {
	cmd := newCancelCmd()

	assert.Equal(t, "cancel <id>", cmd.Use)

	reasonFlag := cmd.Flags().Lookup("reason")
	require.NotNil(t, reasonFlag)
	annotations := reasonFlag.Annotations
	_, hasRequired := annotations["cobra_annotation_bash_completion_one_required_flag"]
	assert.True(t, hasRequired, "--reason should be marked required")

	cascadeFlag := cmd.Flags().Lookup("cascade")
	require.NotNil(t, cascadeFlag)
	assert.Equal(t, "false", cascadeFlag.DefValue)
}

// TestNewReopenCmd_RequiresExactlyOneArg verifies reopen command args.
func TestNewReopenCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newReopenCmd()

	assert.Equal(t, "reopen <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}

// TestNewDeleteCmd_HasCascadeFlag verifies delete command flags.
func TestNewDeleteCmd_HasCascadeFlag(t *testing.T) {
	cmd := newDeleteCmd()

	assert.Equal(t, "delete <id>", cmd.Use)
	cascadeFlag := cmd.Flags().Lookup("cascade")
	require.NotNil(t, cascadeFlag)
	assert.Equal(t, "false", cascadeFlag.DefValue)
}

// TestNewUndeleteCmd_RequiresExactlyOneArg verifies undelete command args.
func TestNewUndeleteCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newUndeleteCmd()

	assert.Equal(t, "undelete <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}

// TestNewDepCmd_HasSubcommands verifies dep command structure.
func TestNewDepCmd_HasSubcommands(t *testing.T) {
	cmd := newDepCmd()

	assert.Equal(t, "dep", cmd.Use)

	subNames := []string{"add", "remove", "show"}
	for _, name := range subNames {
		t.Run(name, func(t *testing.T) {
			found, _, _ := cmd.Find([]string{name})
			require.NotNil(t, found, "subcommand %q should exist", name)
		})
	}
}

// TestNewDepAddCmd_HasTypeFlag verifies dep add command flags.
func TestNewDepAddCmd_HasTypeFlag(t *testing.T) {
	cmd := newDepAddCmd()

	assert.Equal(t, "add <from-id> <to-id>", cmd.Use)
	typeFlag := cmd.Flags().Lookup("type")
	require.NotNil(t, typeFlag)
	assert.Equal(t, "blocks", typeFlag.DefValue)
}

// TestNewDepAddCmd_RequiresExactlyTwoArgs verifies dep add args.
func TestNewDepAddCmd_RequiresExactlyTwoArgs(t *testing.T) {
	cmd := newDepAddCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.Error(t, cmd.Args(cmd, []string{"PROJ-1"}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1", "PROJ-2"}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b", "c"}))
}

// TestNewDepRemoveCmd_HasTypeFlag verifies dep remove command flags.
func TestNewDepRemoveCmd_HasTypeFlag(t *testing.T) {
	cmd := newDepRemoveCmd()

	assert.Equal(t, "remove <from-id> <to-id>", cmd.Use)
	typeFlag := cmd.Flags().Lookup("type")
	require.NotNil(t, typeFlag)
	assert.Equal(t, "blocks", typeFlag.DefValue)
}

// TestNewSearchCmd_HasFilterFlags verifies search command flags.
func TestNewSearchCmd_HasFilterFlags(t *testing.T) {
	cmd := newSearchCmd()

	flags := []string{"status", "assignee", "type", "limit"}
	for _, f := range flags {
		t.Run(f, func(t *testing.T) {
			assert.NotNil(t, cmd.Flags().Lookup(f), "flag --%s should exist", f)
		})
	}
}

// TestNewSearchCmd_LimitDefault_Is50 verifies default search limit.
func TestNewSearchCmd_LimitDefault_Is50(t *testing.T) {
	cmd := newSearchCmd()

	limitFlag := cmd.Flags().Lookup("limit")
	require.NotNil(t, limitFlag)
	assert.Equal(t, "50", limitFlag.DefValue)
}

// TestNewReadyCmd_NoFlagsRequired verifies ready command.
func TestNewReadyCmd_NoFlagsRequired(t *testing.T) {
	cmd := newReadyCmd()

	assert.Equal(t, "ready", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

// TestNewBlockedCmd_NoFlagsRequired verifies blocked command.
func TestNewBlockedCmd_NoFlagsRequired(t *testing.T) {
	cmd := newBlockedCmd()

	assert.Equal(t, "blocked", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

// TestNewStaleCmd_NoFlagsRequired verifies stale command.
func TestNewStaleCmd_NoFlagsRequired(t *testing.T) {
	cmd := newStaleCmd()

	assert.Equal(t, "stale", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

// TestNewOrphansCmd_NoFlagsRequired verifies orphans command.
func TestNewOrphansCmd_NoFlagsRequired(t *testing.T) {
	cmd := newOrphansCmd()

	assert.Equal(t, "orphans", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

// TestNewStatsCmd_NoFlagsRequired verifies stats command.
func TestNewStatsCmd_NoFlagsRequired(t *testing.T) {
	cmd := newStatsCmd()

	assert.Equal(t, "stats", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

// TestNewProgressCmd_RequiresExactlyOneArg verifies progress command args.
func TestNewProgressCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newProgressCmd()

	assert.Equal(t, "progress <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}

// TestNewGCCmd_NoArgs verifies gc command.
func TestNewGCCmd_NoArgs(t *testing.T) {
	cmd := newGCCmd()

	assert.Equal(t, "gc", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
}

// TestNewVerifyCmd_AcceptsOptionalArg verifies verify command args.
func TestNewVerifyCmd_AcceptsOptionalArg(t *testing.T) {
	cmd := newVerifyCmd()

	assert.Equal(t, "verify [id]", cmd.Use)
	assert.NoError(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b"}))
}

// TestNewBackupCmd_RequiresExactlyOneArg verifies backup command args.
func TestNewBackupCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newBackupCmd()

	assert.Equal(t, "backup <path>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"/tmp/backup"}))
}

// TestNewExportCmd_HasFormatFlag verifies export command flags.
func TestNewExportCmd_HasFormatFlag(t *testing.T) {
	cmd := newExportCmd()

	assert.Equal(t, "export", cmd.Use)
	formatFlag := cmd.Flags().Lookup("format")
	require.NotNil(t, formatFlag)
	assert.Equal(t, "json", formatFlag.DefValue)
}

// TestNewImportCmd_RequiresExactlyOneArg verifies import command args.
func TestNewImportCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newImportCmd()

	assert.Equal(t, "import <file>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"data.json"}))
}

// TestNewMigrateCmd_NoArgs verifies migrate command.
func TestNewMigrateCmd_NoArgs(t *testing.T) {
	cmd := newMigrateCmd()
	assert.Equal(t, "migrate", cmd.Use)
}

// TestNewServeCmd_HasAddrAndPortFlags verifies serve command flags.
func TestNewServeCmd_HasAddrAndPortFlags(t *testing.T) {
	cmd := newServeCmd()

	assert.Equal(t, "serve", cmd.Use)

	addrFlag := cmd.Flags().Lookup("addr")
	require.NotNil(t, addrFlag)
	assert.Equal(t, "127.0.0.1", addrFlag.DefValue)

	portFlag := cmd.Flags().Lookup("port")
	require.NotNil(t, portFlag)
	assert.Equal(t, "8377", portFlag.DefValue)
}

// TestNewPromptCmd_RequiresExactlyTwoArgs verifies prompt command args.
func TestNewPromptCmd_RequiresExactlyTwoArgs(t *testing.T) {
	cmd := newPromptCmd()

	assert.Equal(t, "prompt <id> <text>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.Error(t, cmd.Args(cmd, []string{"id"}))
	assert.NoError(t, cmd.Args(cmd, []string{"id", "text"}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b", "c"}))
}

// TestNewAnnotateCmd_RequiresExactlyTwoArgs verifies annotate command args.
func TestNewAnnotateCmd_RequiresExactlyTwoArgs(t *testing.T) {
	cmd := newAnnotateCmd()

	assert.Equal(t, "annotate <id> <text>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"id", "text"}))
}

// TestNewResolveAnnotationCmd_RequiresExactlyTwoArgs verifies resolve-annotation args.
func TestNewResolveAnnotationCmd_RequiresExactlyTwoArgs(t *testing.T) {
	cmd := newResolveAnnotationCmd()

	assert.Equal(t, "resolve-annotation <node-id> <annotation-id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"node-id", "annot-id"}))
}

// TestNewContextCmd_HasMaxTokensFlag verifies context command flags.
func TestNewContextCmd_HasMaxTokensFlag(t *testing.T) {
	cmd := newContextCmd()

	assert.Equal(t, "context <id>", cmd.Use)
	maxTokensFlag := cmd.Flags().Lookup("max-tokens")
	require.NotNil(t, maxTokensFlag)
	assert.Equal(t, "0", maxTokensFlag.DefValue)
}

// TestNewCommentCmd_RequiresExactlyTwoArgs verifies comment command args.
func TestNewCommentCmd_RequiresExactlyTwoArgs(t *testing.T) {
	cmd := newCommentCmd()

	assert.Equal(t, "comment <id> <text>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"id", "text"}))
}

// TestNewDecomposeCmd_RequiresParentID verifies decompose command args.
func TestNewDecomposeCmd_RequiresParentID(t *testing.T) {
	cmd := newDecomposeCmd()

	assert.Equal(t, "decompose <parent-id> [title1 title2...]", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"parent"}))
	assert.NoError(t, cmd.Args(cmd, []string{"parent", "title1"}))
	assert.NoError(t, cmd.Args(cmd, []string{"parent", "t1", "t2", "t3"}))

	// --file flag should exist for JSONL plan ingestion.
	f := cmd.Flags().Lookup("file")
	require.NotNil(t, f)
	assert.Equal(t, "f", f.Shorthand)
}

// TestNewRerunCmd_HasStrategyAndReasonFlags verifies rerun command flags.
func TestNewRerunCmd_HasStrategyAndReasonFlags(t *testing.T) {
	cmd := newRerunCmd()

	assert.Equal(t, "rerun <id>", cmd.Use)

	strategyFlag := cmd.Flags().Lookup("strategy")
	require.NotNil(t, strategyFlag)
	assert.Equal(t, "all", strategyFlag.DefValue)

	reasonFlag := cmd.Flags().Lookup("reason")
	require.NotNil(t, reasonFlag)
	assert.Equal(t, "rerun via CLI", reasonFlag.DefValue)
}

// TestNewRestoreCmd_RequiresExactlyOneArg verifies restore command args.
func TestNewRestoreCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newRestoreCmd()

	assert.Equal(t, "restore <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}

// TestNewConfigCmd_HasSubcommands verifies config command structure.
func TestNewConfigCmd_HasSubcommands(t *testing.T) {
	cmd := newConfigCmd()

	assert.Equal(t, "config", cmd.Use)

	subNames := []string{"get", "set", "delete"}
	for _, name := range subNames {
		t.Run(name, func(t *testing.T) {
			found, _, _ := cmd.Find([]string{name})
			require.NotNil(t, found, "subcommand %q should exist", name)
		})
	}
}

// TestNewConfigGetCmd_RequiresExactlyOneArg verifies config get args.
func TestNewConfigGetCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newConfigGetCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"prefix"}))
	assert.Error(t, cmd.Args(cmd, []string{"a", "b"}))
}

// TestNewConfigSetCmd_RequiresExactlyTwoArgs verifies config set args.
func TestNewConfigSetCmd_RequiresExactlyTwoArgs(t *testing.T) {
	cmd := newConfigSetCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.Error(t, cmd.Args(cmd, []string{"key"}))
	assert.NoError(t, cmd.Args(cmd, []string{"key", "value"}))
}

// TestNewConfigDeleteCmd_RequiresExactlyOneArg verifies config delete args.
func TestNewConfigDeleteCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newConfigDeleteCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"key"}))
}

// TestNewAgentCmd_HasSubcommands verifies agent command structure.
func TestNewAgentCmd_HasSubcommands(t *testing.T) {
	cmd := newAgentCmd()

	assert.Equal(t, "agent", cmd.Use)

	subNames := []string{"state", "heartbeat", "work"}
	for _, name := range subNames {
		t.Run(name, func(t *testing.T) {
			found, _, _ := cmd.Find([]string{name})
			require.NotNil(t, found, "subcommand %q should exist", name)
		})
	}
}

// TestNewAgentStateCmd_HasSetFlag verifies agent state command flags.
func TestNewAgentStateCmd_HasSetFlag(t *testing.T) {
	cmd := newAgentStateCmd()

	assert.Equal(t, "state <agent-id>", cmd.Use)
	setFlag := cmd.Flags().Lookup("set")
	require.NotNil(t, setFlag)
}

// TestNewAgentHeartbeatCmd_RequiresExactlyOneArg verifies heartbeat args.
func TestNewAgentHeartbeatCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newAgentHeartbeatCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"agent-1"}))
}

// TestNewAgentWorkCmd_RequiresExactlyOneArg verifies agent work args.
func TestNewAgentWorkCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newAgentWorkCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"agent-1"}))
}

// TestNewSessionCmd_HasSubcommands verifies session command structure.
func TestNewSessionCmd_HasSubcommands(t *testing.T) {
	cmd := newSessionCmd()

	assert.Equal(t, "session", cmd.Use)

	subNames := []string{"start", "end", "summary"}
	for _, name := range subNames {
		t.Run(name, func(t *testing.T) {
			found, _, _ := cmd.Find([]string{name})
			require.NotNil(t, found, "subcommand %q should exist", name)
		})
	}
}

// TestNewSessionStartCmd_RequiresExactlyOneArg verifies session start args.
func TestNewSessionStartCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newSessionStartCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"agent-1"}))
}

// TestNewSessionEndCmd_RequiresExactlyOneArg verifies session end args.
func TestNewSessionEndCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newSessionEndCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"agent-1"}))
}

// TestNewSessionSummaryCmd_RequiresExactlyOneArg verifies session summary args.
func TestNewSessionSummaryCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newSessionSummaryCmd()

	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"agent-1"}))
}

// TestNewInitCmd_HasPrefixFlag verifies init command flags.
func TestNewInitCmd_HasPrefixFlag(t *testing.T) {
	cmd := newInitCmd()

	assert.Equal(t, "init", cmd.Use)
	prefixFlag := cmd.Flags().Lookup("prefix")
	require.NotNil(t, prefixFlag)
	assert.Equal(t, "PROJ", prefixFlag.DefValue)
}

// TestNewDepShowCmd_RequiresExactlyOneArg verifies dep show args.
func TestNewDepShowCmd_RequiresExactlyOneArg(t *testing.T) {
	cmd := newDepShowCmd()

	assert.Equal(t, "show <id>", cmd.Use)
	assert.Error(t, cmd.Args(cmd, []string{}))
	assert.NoError(t, cmd.Args(cmd, []string{"PROJ-1"}))
}
