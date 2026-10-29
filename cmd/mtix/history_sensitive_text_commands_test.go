// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func cleanupCommandSection(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path)) //nolint:gosec // fixed repository documentation paths
	require.NoError(t, err)
	const heading = "Removing sensitive text (0.6.0)"
	start := strings.Index(string(data), heading)
	require.NotEqual(t, -1, start, "%s needs the cleanup guide", path)
	body := string(data)[start+len(heading):]
	if end := regexp.MustCompile(`(?m)^#{1,4} `).FindStringIndex(body); end != nil {
		body = body[:end[0]]
	}
	return body
}

// Validate the command prefix before shell redirection, without executing it.
// The examples use one-word values, matching mtixInvocations' tokenizer.
func parsedCleanupCommand(args []string) (string, error) {
	for i, word := range args {
		if word == ">" || word == ">>" || word == "<" {
			args = args[:i]
			break
		}
	}
	cmd, rest, err := newRootCmd().Find(args)
	if err != nil {
		return "", err
	}
	if err := cmd.ParseFlags(rest); err != nil {
		return "", err
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return "", err
	}
	if err := cleanupConfigArguments(cmd.CommandPath(), cmd.Flags().Args()); err != nil {
		return "", err
	}
	return cmd.CommandPath(), nil
}

func cleanupConfigArguments(command string, args []string) error {
	switch command {
	case "mtix config get":
		if len(args) != 1 || args[0] != "history.export" {
			return fmt.Errorf("cleanup config get must read history.export")
		}
	case "mtix config set":
		if len(args) != 2 || args[0] != "history.export" || args[1] != "false" {
			return fmt.Errorf("cleanup config set must set history.export to false")
		}
	}
	return nil
}

func TestHistorySensitiveTextCommands_ParseInThisBuild(t *testing.T) {
	for _, path := range []string{
		"USERMANUAL.md", "internal/docs/templates/skills/sync.md.tmpl",
		".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
	} {
		t.Run(path, func(t *testing.T) {
			seen := map[string]bool{}
			for _, args := range mtixInvocations(cleanupCommandSection(t, path)) {
				command, err := parsedCleanupCommand(args)
				require.NoErrorf(t, err, "mtix %s", strings.Join(args, " "))
				seen[command] = true
			}
			for _, command := range []string{"mtix update", "mtix config set", "mtix config get", "mtix export"} {
				require.True(t, seen[command], "%s must be demonstrated", command)
			}
		})
	}
}

func TestHistorySensitiveTextCommands_ParserRejectsInvalidSyntax(t *testing.T) {
	for _, args := range [][]string{{"update", "PROJ-1", "--description", "Clean"}, {"config", "set", "history.export", "false"}, {"config", "get", "history.export"}, {"export", ">", ".mtix/tasks-clean.json"}} {
		_, err := parsedCleanupCommand(args)
		require.NoError(t, err)
	}
	for _, args := range [][]string{{"unsupported-history-command"}, {"export", "--no-such-export-flag"}, {"export", "extra.json"}, {"config", "get"}, {"config", "get", "history.exports"}, {"config", "set", "history.exports", "false"}, {"config", "set", "history.export", "off"}, {"config", "set", "history.export", "true"}} {
		_, err := parsedCleanupCommand(args)
		require.Error(t, err)
	}
}
