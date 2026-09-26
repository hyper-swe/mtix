// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Mutation-time warning of MTIX-95.12 (acceptance 3): with a hub configured,
// a mutation whose sync event payload is over the 64 KB wire cap still
// succeeds locally, prints one warning line naming the field and the limit
// (on stderr for the CLI, as a text block of the tool result for MCP), and
// its event is then held at push.

// unreachableHubDSN configures a hub for a test; nothing in these tests
// contacts it.
const unreachableHubDSN = "postgres://mtix@127.0.0.1:1/hub?sslmode=disable"

// setHub configures a hub through MTIX_SYNC_DSN, or leaves none configured.
func setHub(t *testing.T, hub bool) {
	t.Helper()
	if hub {
		t.Setenv(transport.EnvDSN, unreachableHubDSN)
		return
	}
	t.Setenv(transport.EnvDSN, "")
}

// executeRoot runs the whole mtix command tree with args, as the binary
// does (pre-run, command, post-run), and returns what cobra wrote to
// stderr.
func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetCloseOnce()
	root := newRootCmd()
	var errOut bytes.Buffer
	root.SetErr(&errOut)
	root.SetOut(io.Discard)
	root.SetArgs(args)
	err := root.Execute()
	return errOut.String(), err
}

// payloadWarnProject makes a fresh project with the node TEST-1 in a temp
// directory that is the working directory for the rest of the test.
func payloadWarnProject(t *testing.T, hub bool) {
	t.Helper()
	saveAndResetApp(t)
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "config.yaml"),
		[]byte("prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"), 0o644))
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	require.NoError(t, os.Chdir(tmpDir))
	setHub(t, hub)
	_, err = executeRoot(t, "create", "base node")
	require.NoError(t, err)
}

// reopenApp opens the project's store again after a command closed it.
func reopenApp(t *testing.T) {
	t.Helper()
	resetCloseOnce()
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	t.Cleanup(func() {
		if app.store != nil {
			_ = app.store.Close()
		}
	})
}

// wireCapWarnings returns the lines of out that warn about the wire cap.
func wireCapWarnings(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "65536") {
			lines = append(lines, l)
		}
	}
	return lines
}

// requireWireCapWarning asserts out holds exactly one warning line, naming
// node, field and the 65536-byte limit.
func requireWireCapWarning(t *testing.T, out, node, field string) {
	t.Helper()
	lines := wireCapWarnings(out)
	require.Len(t, lines, 1, "one warning line: %q", out)
	require.True(t, strings.HasPrefix(lines[0], "WARN"), lines[0])
	require.Contains(t, lines[0], node)
	require.Regexp(t, regexp.MustCompile(`\b`+field+`\b`), lines[0])
}

// requireHeldAtPush pushes to a fake hub and asserts the event of op for
// node is held with source push and a reason naming field.
func requireHeldAtPush(t *testing.T, node string, op model.OpType, field string) {
	t.Helper()
	id := eventIDFor(t, node, op)
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, newFakePushHub(), app.store)
	require.NoError(t, err)
	q, ok := quarantined(t)[id]
	require.True(t, ok, "the event is held at push")
	require.Equal(t, "push", q.Source)
	require.Contains(t, q.Reason, field)
}

// TestMutationCommand_OversizedPayloadWithHub_WarnsFieldAndLimit: CLI
// mutations over the wire cap succeed, print one warning line naming the
// field and the limit when a hub is configured, and their event is held at
// push; none is printed under the cap or without a hub.
func TestMutationCommand_OversizedPayloadWithHub_WarnsFieldAndLimit(t *testing.T) {
	big := overWireCap()
	tests := []struct {
		name  string
		hub   bool
		args  []string
		node  string
		op    model.OpType
		field string // "" when no warning is expected
		plan  string // written to plan.jsonl in the project before the command
	}{
		{"create with a prompt over the cap", true, []string{"create", "big", "--prompt", big}, "TEST-2", model.OpCreateNode, "prompt", ""},
		{"update with a prompt over the cap", true, []string{"update", "TEST-1", "--prompt", big}, "TEST-1", model.OpSetPrompt, "prompt", ""},
		{"prompt command over the cap", true, []string{"prompt", "TEST-1", big}, "TEST-1", model.OpSetPrompt, "prompt", ""},
		{"comment over the cap", true, []string{"comment", "TEST-1", big}, "TEST-1", model.OpComment, "comment", ""},
		{"annotate over the cap", true, []string{"annotate", "TEST-1", big}, "TEST-1", model.OpComment, "comment", ""},
		{"update with acceptance over the cap", true, []string{"update", "TEST-1", "--acceptance", big}, "TEST-1", model.OpSetAcceptance, "acceptance", ""},
		{"decompose with a child prompt over the cap", true, []string{"decompose", "TEST-1", "--file", "plan.jsonl"},
			"TEST-1.1", model.OpCreateNode, "prompt", decomposeLineOverCap()},
		{"create whose description is the largest field", true, []string{"create", "wide",
			"--description", strings.Repeat("d", model.MaxDescriptionSize-64), "--prompt", strings.Repeat("p", 20*1024)},
			"TEST-2", model.OpCreateNode, "description", ""},
		{"prompt under the cap", true, []string{"update", "TEST-1", "--prompt", strings.Repeat("p", 8*1024)}, "", "", "", ""},
		{"no hub configured", false, []string{"update", "TEST-1", "--prompt", big}, "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payloadWarnProject(t, tt.hub)
			if tt.plan != "" {
				require.NoError(t, os.WriteFile("plan.jsonl", []byte(tt.plan), 0o644))
			}
			out, err := executeRoot(t, tt.args...)
			require.NoError(t, err, "the mutation succeeds locally")
			if tt.field == "" {
				require.Empty(t, wireCapWarnings(out))
				return
			}
			requireWireCapWarning(t, out, tt.node, tt.field)
			reopenApp(t)
			requireHeldAtPush(t, tt.node, tt.op, tt.field)
		})
	}
}

// TestMCPTool_OversizedPayloadWithHub_WarnsFieldAndLimit: MCP mutations over
// the wire cap succeed and add one warning text block naming the field and
// the limit to the tool result when a hub is configured; none is added under
// the cap or without a hub.
func TestMCPTool_OversizedPayloadWithHub_WarnsFieldAndLimit(t *testing.T) {
	big := overWireCap()
	tests := []struct {
		name  string
		hub   bool
		tool  string
		args  map[string]any
		node  string
		field string // "" when no warning is expected
	}{
		{"mtix_create with a prompt over the cap", true, "mtix_create", map[string]any{"title": "big", "prompt": big}, "TEST-2", "prompt"},
		{"mtix_prompt over the cap", true, "mtix_prompt", map[string]any{"id": "TEST-1", "text": big, "author": "agent"}, "TEST-1", "prompt"},
		{"mtix_annotate over the cap", true, "mtix_annotate", map[string]any{"id": "TEST-1", "text": big, "author": "agent"}, "TEST-1", "comment"},
		{"under the cap", true, "mtix_prompt", map[string]any{"id": "TEST-1", "text": "short", "author": "agent"}, "", ""},
		{"no hub configured", false, "mtix_prompt", map[string]any{"id": "TEST-1", "text": big, "author": "agent"}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setHub(t, tt.hub)
			initTestApp(t)
			require.NoError(t, runCreate("base node", "", "", 3, "", "", "", "", ""))
			reg := mcp.NewToolRegistry()
			registerMCPTools(reg)
			raw, err := json.Marshal(tt.args)
			require.NoError(t, err)

			res, err := reg.Call(context.Background(), tt.tool, raw)
			require.NoError(t, err, "the mutation succeeds locally")
			require.False(t, res.IsError)
			var texts []string
			for _, c := range res.Content {
				texts = append(texts, c.Text)
			}
			all := strings.Join(texts, "\n")
			if tt.field == "" {
				require.Empty(t, wireCapWarnings(all))
				return
			}
			requireWireCapWarning(t, all, tt.node, tt.field)
			require.NotContains(t, texts[0], "65536", "the tool's own result comes first, unchanged")
		})
	}
}

// TestHubConfigured_DSNSources_ReportAHub: a hub counts as configured when
// MTIX_SYNC_DSN or .mtix/secrets provides a DSN, even one transport.Source
// refuses to use (a secrets file with the wrong mode); not otherwise.
func TestHubConfigured_DSNSources_ReportAHub(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		secrets string
		mode    os.FileMode
		noDir   bool
		want    bool
	}{
		{"nothing configured", "", "", 0, false, false},
		{"MTIX_SYNC_DSN set", unreachableHubDSN, "", 0, false, true},
		{"secrets file", "", unreachableHubDSN, 0o600, false, true},
		{"secrets file with the wrong mode", "", unreachableHubDSN, 0o644, false, true},
		{"not in a project", unreachableHubDSN, "", 0, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, tt.env)
			mtixDir := t.TempDir()
			if tt.secrets != "" {
				path := filepath.Join(mtixDir, transport.SecretsFilename)
				require.NoError(t, os.WriteFile(path, []byte(tt.secrets), tt.mode))
				require.NoError(t, os.Chmod(path, tt.mode))
			}
			if tt.noDir {
				mtixDir = ""
			}
			require.Equal(t, tt.want, hubConfigured(mtixDir))
			require.Equal(t, tt.want, newPayloadWarnings(mtixDir) != nil)
		})
	}
}

// decomposeLineOverCap is one decompose JSONL line whose child's create
// event is over the wire cap. decompose reads a line of at most 64 KB
// (bufio.Scanner), so the prompt fills the line almost to that limit; the
// fields the create event adds (parent id, node type, priority, creator)
// take its payload past validator.MaxPayloadBytes.
func decomposeLineOverCap() string {
	const wrap = len(`{"title":"t","prompt":""}`)
	return `{"title":"t","prompt":"` + strings.Repeat("p", validator.MaxPayloadBytes-wrap-8) + `"}` + "\n"
}

// TestRunImport_OversizedPrompt_NoSyncEventNoWarning pins why `mtix import`
// (and the automatic import of .mtix/tasks.json, which uses the same store
// import) carries no wire-cap warning: an import writes tasks without sync
// events, so nothing it writes is pushed, held or warned about, whatever
// its field sizes (MTIX-95.12 round 2, review r1 R19).
func TestRunImport_OversizedPrompt_NoSyncEventNoWarning(t *testing.T) {
	ctx := context.Background()
	setHub(t, true)
	initTestApp(t)
	require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
	data, err := app.store.Export(ctx, "", "")
	require.NoError(t, err)
	body, err := json.Marshal(data)
	require.NoError(t, err)
	var export map[string]any
	require.NoError(t, json.Unmarshal(body, &export))
	nodes, ok := export["nodes"].([]any)
	require.True(t, ok && len(nodes) == 1, "one exported task")
	nodes[0].(map[string]any)["prompt"] = overWireCap()
	board, err := json.Marshal(export)
	require.NoError(t, err)

	initTestApp(t)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, board, 0o644))
	require.NoError(t, runImport(path, importFlags{mode: "merge", recomputeChecksum: true}))
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Prompt, len(overWireCap()), "the import wrote the task")
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events`), "an import writes no sync event")
	require.NotNil(t, app.payloadWarnings, "a hub is configured")
	require.Empty(t, app.payloadWarnings.Drain(), "so there is nothing to warn about")
}
