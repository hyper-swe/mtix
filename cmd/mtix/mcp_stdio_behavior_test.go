// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests exercise CLI startup and finite stock-main MCP transactions.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestMCPStdio_StartupRefusals(t *testing.T) {
	t.Run("missing-project", func(t *testing.T) {
		saveAndResetApp(t)
		cmd := newMCPCmd()
		cmd.SetArgs([]string{})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		text, err := cliBehaviorOutput(t, cmd.Execute)
		require.ErrorContains(t, err, "not in an mtix project")
		require.Empty(t, text)
	})
	for _, collision := range []string{"logs", "mtix.log"} {
		t.Run(collision, func(t *testing.T) {
			initTestApp(t)
			path := filepath.Join(app.mtixDir, "logs")
			if collision == "logs" {
				require.NoError(t, os.WriteFile(path, []byte("sentinel"), 0o600))
			} else {
				require.NoError(t, os.MkdirAll(filepath.Join(path, collision), 0o700))
			}
			for _, flags := range [][]string{{}, {"--read-only=true"}, {"--read-only=false"}} {
				_, err := app.configSvc.Set("mcp.read_only", "true")
				require.NoError(t, err)
				cmd := newMCPCmd()
				cmd.SetArgs(flags)
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				text, runErr := cliBehaviorOutput(t, cmd.Execute)
				require.ErrorContains(t, runErr, "create log file")
				require.Empty(t, text)
			}
			if collision == "logs" {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "sentinel", string(data))
			} else {
				entries, err := os.ReadDir(filepath.Join(path, collision))
				require.NoError(t, err)
				require.Empty(t, entries)
			}
		})
	}
}

func TestMCPStdio_ProjectCallbacks(t *testing.T) {
	old, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.Chdir(old)) })
	for _, dir := range []string{"", t.TempDir(), filepath.Join(t.TempDir(), "absent")} {
		cmd := newMCPCmd()
		require.NoError(t, cmd.ParseFlags([]string{"--project", dir}))
		err := cmd.PreRunE(cmd, nil)
		if strings.HasSuffix(dir, "absent") {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			cwd, getErr := os.Getwd()
			require.NoError(t, getErr)
			expected := old
			if dir != "" {
				expected = dir
			}
			resolved, resolveErr := filepath.EvalSymlinks(expected)
			require.NoError(t, resolveErr)
			require.Equal(t, resolved, cwd)
		}
		require.NoError(t, os.Chdir(old))
	}
}

func TestMCPStdio_ChildMain(t *testing.T) {
	if os.Getenv("MTIX_STDIO_BEHAVIOR_CHILD") != "1" {
		return
	}
	var args []string
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("MTIX_STDIO_BEHAVIOR_ARGS")), &args))
	os.Args = append([]string{"mtix"}, args...)
	main()
	os.Exit(0) //nolint:gocritic // stock main completed; prevent the test harness from contaminating protocol stdout
}

type stdioBehaviorCase struct {
	config   bool
	flag     string
	readOnly bool
}

func TestMCPStdio_ReadOnlyPrecedenceAndDurableMirror(t *testing.T) {
	cases := []stdioBehaviorCase{{true, "", true}, {false, "", false}, {false, "--read-only=true", true}, {true, "--read-only=false", false}}
	for i, tc := range cases {
		t.Run(strconv.Itoa(i+1), func(t *testing.T) {
			initTestApp(t)
			node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Seed task", Creator: "seed"})
			require.NoError(t, err)
			dir := app.mtixDir
			before := stdioJournalCount(t, app.store)
			require.NoError(t, app.store.Close())
			config := fmt.Sprintf("prefix: TEST\nmcp:\n  read_only: %t\nsync:\n  auto_sync: false\n", tc.config)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600))
			args := []string{"-C", filepath.Dir(dir), "mcp"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			responses := stdioRunChild(t, args, stdioTranscript(t, node.ID))
			stdioCheckResponses(t, responses, node, tc.readOnly)
			stdioCheckPersistence(t, dir, before, tc.readOnly)
			log, err := os.ReadFile(filepath.Join(dir, "logs", "mtix.log"))
			require.NoError(t, err)
			require.Contains(t, string(log), "mtix MCP server starting")
			require.Contains(t, string(log), "read_only="+strconv.FormatBool(tc.readOnly))
		})
	}
}

func stdioTranscript(t *testing.T, id string) []byte {
	t.Helper()
	messages := []map[string]any{
		{"id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "mtix-test", "version": "1"}}},
		{"method": "notifications/initialized"},
		{"id": 2, "method": "tools/list", "params": map[string]any{}},
		{"id": 3, "method": "tools/call", "params": map[string]any{"name": "mtix_show", "arguments": map[string]any{"id": id}}},
		{"id": 4, "method": "tools/call", "params": map[string]any{"name": "mtix_create", "arguments": map[string]any{"title": "Child task", "description": "durable description", "prompt": "instructions", "acceptance": "exact acceptance", "priority": 2, "creator": "stdio-worker"}}},
		{"id": 5, "method": "tools/call", "params": map[string]any{"name": "mtix_list", "arguments": map[string]any{"project": "TEST"}}},
	}
	var data []byte
	for _, message := range messages {
		message["jsonrpc"] = "2.0"
		line, err := json.Marshal(message)
		require.NoError(t, err)
		data = append(append(data, line...), '\n')
	}
	return data
}

func stdioRunChild(t *testing.T, args []string, transcript []byte) []map[string]json.RawMessage {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "input.jsonl")
	require.NoError(t, os.WriteFile(input, transcript, 0o600))
	in, err := os.Open(input)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, in.Close()) })
	out := stdioOwnedFile(t, filepath.Join(dir, "output.jsonl"))
	stderr := stdioOwnedFile(t, filepath.Join(dir, "stderr.txt"))
	binary, err := os.Executable()
	require.NoError(t, err)
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestMCPStdio_ChildMain$")
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, in, out, stderr
	cmd.Env = stdioChildEnvironment(string(encoded))
	started, joined := false, false
	t.Cleanup(func() {
		cancel()
		if started && !joined {
			_ = cmd.Wait()
		}
	})
	require.NoError(t, cmd.Start())
	started = true
	waitErr := cmd.Wait()
	joined = true
	diagnostic, readErr := os.ReadFile(stderr.Name())
	require.NoError(t, readErr)
	require.NoError(t, waitErr, string(diagnostic))
	stdioCheckDiagnostics(t, diagnostic)
	data, err := os.ReadFile(out.Name())
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 5)
	responses := make([]map[string]json.RawMessage, len(lines))
	for i, line := range lines {
		require.NoError(t, json.Unmarshal([]byte(line), &responses[i]))
		require.JSONEq(t, strconv.Itoa(i+1), string(responses[i]["id"]))
		require.JSONEq(t, `"2.0"`, string(responses[i]["jsonrpc"]))
		require.NotContains(t, responses[i], "error")
	}
	return responses
}

func stdioCheckDiagnostics(t *testing.T, diagnostic []byte) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(string(diagnostic)), "\n") {
		if line == "" || line == "warning: GOCOVERDIR not set, no coverage data emitted" {
			continue
		}
		require.Contains(t, line, "level=INFO")
		require.Contains(t, line, "msg=sync_export_completed")
		require.NotContains(t, line, "jsonrpc")
	}
}

func stdioOwnedFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	return f
}

func stdioChildEnvironment(args string) []string {
	var env []string
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "MTIX_") || key == "GOCOVERDIR" {
			continue
		}
		env = append(env, value)
	}
	return append(env, "MTIX_STDIO_BEHAVIOR_CHILD=1", "MTIX_STDIO_BEHAVIOR_ARGS="+args, "MTIX_AUTHOR_ID=stdio-worker")
}

func stdioResult(t *testing.T, response map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response["result"], &result))
	return result
}

func stdioContent(t *testing.T, result map[string]json.RawMessage) string {
	t.Helper()
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(result["content"], &content))
	require.Len(t, content, 1)
	require.Equal(t, "text", content[0].Type)
	return content[0].Text
}

func stdioCheckInitialize(t *testing.T, response map[string]json.RawMessage) {
	t.Helper()
	var result mcp.InitializeResult
	require.NoError(t, json.Unmarshal(response["result"], &result))
	require.Equal(t, "2024-11-05", result.ProtocolVersion)
	require.Equal(t, "mtix", result.ServerInfo.Name)
	require.Equal(t, version, result.ServerInfo.Version)
	require.NotNil(t, result.Capabilities.Tools)
}

func stdioCheckResponses(t *testing.T, responses []map[string]json.RawMessage, seed *model.Node, readOnly bool) {
	t.Helper()
	stdioCheckInitialize(t, responses[0])
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(responses[1]["result"], &tools))
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	require.Contains(t, names, "mtix_show")
	require.Equal(t, !readOnly, slicesContainString(names, "mtix_create"))
	var shown model.Node
	require.NoError(t, json.Unmarshal([]byte(stdioContent(t, stdioResult(t, responses[2]))), &shown))
	require.Equal(t, seed.ID, shown.ID)
	require.Equal(t, seed.Title, shown.Title)
	created := stdioResult(t, responses[3])
	if readOnly {
		require.JSONEq(t, "true", string(created["isError"]))
		require.Contains(t, stdioContent(t, created), "read-only")
	} else {
		require.NotEqual(t, "true", string(created["isError"]))
		require.Contains(t, stdioContent(t, created), "Child task")
	}
	var listed struct {
		Nodes []model.Node `json:"nodes"`
		Total int          `json:"total"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdioContent(t, stdioResult(t, responses[4]))), &listed))
	expected := 2
	if readOnly {
		expected = 1
	}
	require.Equal(t, expected, listed.Total)
	require.Len(t, listed.Nodes, expected)
}

func slicesContainString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stdioJournalCount(t *testing.T, st *sqlite.Store) int {
	t.Helper()
	var count int
	// Count the owned journal before and after the finite child transaction.
	require.NoError(t, st.ReadDB().QueryRow("SELECT COUNT(*) FROM sync_events").Scan(&count))
	return count
}

func stdioCheckPersistence(t *testing.T, dir string, before int, readOnly bool) {
	t.Helper()
	st, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	var nodes int
	// Inspect durable task fields independently of protocol rendering.
	require.NoError(t, st.ReadDB().QueryRow("SELECT COUNT(*) FROM nodes").Scan(&nodes))
	expected := 2
	if readOnly {
		expected = 1
	}
	require.Equal(t, expected, nodes)
	if readOnly {
		require.Equal(t, before, stdioJournalCount(t, st))
		_, statErr := os.Stat(filepath.Join(dir, "tasks.json"))
		require.ErrorIs(t, statErr, os.ErrNotExist)
		return
	}
	var id, uid, hash, project, creator, description, prompt, acceptance string
	var priority int
	// Bound the title selector; all field values must survive the actual mutation.
	require.NoError(t, st.ReadDB().QueryRow(`SELECT id, uid, content_hash, project, creator, description, prompt, acceptance, priority FROM nodes WHERE title = ?`, "Child task").Scan(&id, &uid, &hash, &project, &creator, &description, &prompt, &acceptance, &priority))
	require.NotEmpty(t, uid)
	require.NotEmpty(t, hash)
	require.Equal(t, "TEST", project)
	require.Equal(t, "stdio-worker", creator)
	require.Equal(t, "durable description", description)
	require.Equal(t, "instructions", prompt)
	require.Equal(t, "exact acceptance", acceptance)
	require.Equal(t, 2, priority)
	var events int
	// The newly persisted task must have its own journal entry.
	require.NoError(t, st.ReadDB().QueryRow("SELECT COUNT(*) FROM sync_events WHERE node_id = ?", id).Scan(&events))
	require.Positive(t, events)
	require.Greater(t, stdioJournalCount(t, st), before)
	stdioCheckMirror(t, dir, &model.Node{ID: id, UID: uid, ContentHash: hash, Project: project, Creator: creator, Description: description, Prompt: prompt, Acceptance: acceptance, Priority: model.Priority(priority)})
}

func stdioCheckMirror(t *testing.T, dir string, expected *model.Node) {
	t.Helper()
	mirror, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	var board sqlite.ExportData
	require.NoError(t, json.Unmarshal(mirror, &board))
	require.Len(t, board.Nodes, 2)
	var titles []string
	for _, node := range board.Nodes {
		titles = append(titles, node.Title)
	}
	require.ElementsMatch(t, []string{"Seed task", "Child task"}, titles)
	for _, node := range board.Nodes {
		if node.ID != expected.ID {
			continue
		}
		require.Equal(t, "Child task", node.Title)
		require.Equal(t, expected.UID, node.UID)
		require.Equal(t, expected.ContentHash, node.ContentHash)
		require.Equal(t, expected.Project, node.Project)
		require.Equal(t, expected.Creator, node.Creator)
		require.Equal(t, expected.Description, node.Description)
		require.Equal(t, expected.Prompt, node.Prompt)
		require.Equal(t, expected.Acceptance, node.Acceptance)
		require.Equal(t, int(expected.Priority), node.Priority)
		return
	}
	t.Fatal("created task missing from durable mirror")
}
