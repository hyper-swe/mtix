// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

// MTIX-146: `mtix init <prefix>` is documented; the positional is the prefix.
func TestInitCmd_PositionalPrefix_InitializesWithIt(t *testing.T) {
	dir := chdirTemp(t)
	_, err := executeCmd(newInitCmd(), "ABC")
	require.NoError(t, err)
	cfg, err := os.ReadFile(filepath.Join(dir, ".mtix", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfg), "ABC")
}

func TestInitCmd_PositionalAndFlag_MustAgree(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"same value", []string{"ABC", "--prefix", "ABC"}, false},
		{"conflicting value", []string{"ABC", "--prefix", "XYZ"}, true},
		{"two positionals", []string{"ABC", "XYZ"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chdirTemp(t)
			_, err := executeCmd(newInitCmd(), tt.args...)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// MTIX-146: `mtix plugin install <target>` is documented; the positional is the target.
func TestPluginInstallCmd_PositionalTarget_InstallsThatTarget(t *testing.T) {
	dir := chdirTemp(t)
	_, err := executeCmd(newPluginInstallCmd(), "pi")
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(dir, "AGENTS.md"))
	assert.NoError(t, statErr, "target pi writes AGENTS.md; the default target would not")
}

func TestPluginInstallCmd_PositionalAndFlag_MustAgree(t *testing.T) {
	chdirTemp(t)
	_, err := executeCmd(newPluginInstallCmd(), "pi", "--target", "codex")
	require.Error(t, err)
	_, err = executeCmd(newPluginInstallCmd(), "pi", "codex")
	require.Error(t, err)
}

// MTIX-146: `mtix blocked <id>` is documented as "see blockers of <id>".
func TestBlockedCmd_PositionalID_ShowsBlockersOfThatNode(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Lonely", "", "", 3, "", "", "", "", ""))
	var err error
	out := captureStdout(t, func() { _, err = executeCmd(newBlockedCmd(), "TEST-1") })
	require.NoError(t, err)
	assert.Contains(t, out, "No blockers for TEST-1")
}

func TestBlockedCmd_PositionalIDWithProjectScope_ReturnsError(t *testing.T) {
	initTestApp(t)
	_, err := executeCmd(newBlockedCmd(), "TEST-1", "--all-projects")
	require.Error(t, err)
}

// shippedDocInvocations returns every "mtix <words>" fragment inside inline
// code or fenced blocks of the shipped agent docs.
func shippedDocInvocations(t *testing.T) []string {
	t.Helper()
	re := regexp.MustCompile("`?(mtix (?:blocked|init|plugin install)[^`\n]*)")
	var found []string
	for _, root := range []string{"../../internal/docs/templates", "../../.claude-plugin", "../../USERMANUAL.md", "../../README.md"} {
		require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			b, readErr := os.ReadFile(p)
			require.NoError(t, readErr)
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				found = append(found, m[1])
			}
			return nil
		}))
	}
	return found
}

// MTIX-146: scoped doc-invocation check. Every documented `mtix blocked`,
// `mtix init` and `mtix plugin install` line must parse against the command tree.
func TestShippedDocs_BlockedInitPluginInstall_ParseAgainstCommandTree(t *testing.T) {
	invocations := shippedDocInvocations(t)
	require.NotEmpty(t, invocations)
	for _, inv := range invocations {
		// A README table pads the command and then describes it after two spaces.
		fields := strings.Fields(strings.SplitN(inv, "  ", 2)[0])
		var tokens []string
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "#") || strings.HasPrefix(f, "(") || strings.HasPrefix(f, "[") || strings.HasPrefix(f, "|") {
				break
			}
			tokens = append(tokens, strings.Trim(f, ".,;:"))
		}
		root := newRootCmd()
		cmd, rest, err := root.Find(tokens)
		require.NoError(t, err, inv)
		require.NoError(t, cmd.ParseFlags(rest), inv)
		assert.NoError(t, cmd.Args(cmd, cmd.Flags().Args()), "documented invocation does not parse: %q", inv)
	}
}

func TestBlockedCmd_UnknownID_ReturnsNotFound(t *testing.T) {
	initTestApp(t)
	_, err := executeCmd(newBlockedCmd(), "TEST-999")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestBlockedCmd_PositionalID_JSONIsDependencyEdges(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	require.NoError(t, runCreate("Blocker", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Blocked", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
	var err error
	out := captureStdout(t, func() { _, err = executeCmd(newBlockedCmd(), "TEST-2") })
	require.NoError(t, err)
	var edges []struct {
		FromID string `json:"from_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &edges))
	require.Len(t, edges, 1)
	assert.Equal(t, "TEST-1", edges[0].FromID)
}
