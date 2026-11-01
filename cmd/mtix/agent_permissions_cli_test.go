// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func permissionDraftClearMarkers(t *testing.T) {
	t.Helper()
	for _, entry := range hostguard.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "CLAUDECODE" || key == "CODEX_THREAD_ID" || strings.HasPrefix(key, "CLAUDE_CODE_") || strings.HasPrefix(key, "CODEX_SANDBOX") || strings.HasPrefix(key, "PI_") {
			t.Setenv(key, "")
		}
	}
}

func permissionDraftState(t *testing.T) (*oplocal.State, string) {
	t.Helper()
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	dir, err := state.Path()
	require.NoError(t, err)
	return state, dir
}

func permissionDraftLog(t *testing.T) []map[string]any {
	t.Helper()
	_, dir := permissionDraftState(t)
	path := filepath.Join(dir, "confirmations.jsonl")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, byte('\n'), data[len(data)-1])
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var row map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &row))
		rows = append(rows, row)
	}
	return rows
}

func permissionDraftIdentity(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".mtix", "board-id"))
	require.NoError(t, err)
	id := strings.TrimSpace(string(data))
	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	assert.Equal(t, parsed.String(), id)
	assert.NotEqual(t, uuid.Nil, parsed)
	return id
}

func permissionDraftRegister(t *testing.T, root, agent string) {
	t.Helper()
	out := doctorCLIExecute(t, root, "", "agent", "register", agent, "--harness", "fixture", "--session", "fixture-session")
	require.Equal(t, 0, out.exit, out.stderr)
}

func TestAgentAllow_NoTTYWithoutYes_Refused(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionDraftRegister(t, root, "permission-fixture")
	out := doctorCLIExecute(t, root, "", "agent", "allow", "permission-fixture")
	assert.Equal(t, 1, out.exit)
	assert.Contains(t, strings.ToLower(out.stderr), "terminal")
	assert.NotContains(t, out.stderr, "unknown command")
	_, dir := permissionDraftState(t)
	_, err := os.Stat(filepath.Join(dir, "grants.json"))
	assert.True(t, os.IsNotExist(err), "refused allow must not create grants")
}

func TestAgentAllowYes_UnderHarnessMarker_AcceptedRecordedAndFlagged(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	t.Setenv("CODEX_THREAD_ID", "synthetic-thread-value-not-for-log")
	permissionDraftRegister(t, root, "permission-fixture")
	_, absent := os.Stat(filepath.Join(root, ".mtix", "board-id"))
	require.True(t, os.IsNotExist(absent), "allow must cover a legacy board with no identity")
	out := doctorCLIExecute(t, root, "", "agent", "allow", "permission-fixture", "--yes")
	require.Equal(t, 0, out.exit, out.stderr)
	rows := permissionDraftLog(t)
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "permission-fixture", row["agent_id"])
	assert.Equal(t, "allow", row["action"])
	assert.Equal(t, "yes", row["mode"])
	assert.Equal(t, permissionDraftIdentity(t, root), row["board_id"])
	assert.NotEmpty(t, row["host_id"])
	assert.NotEmpty(t, row["source"])
	stamp, ok := row["time"].(string)
	require.True(t, ok)
	_, err := time.Parse(time.RFC3339Nano, stamp)
	require.NoError(t, err)
	assert.Equal(t, []any{"CODEX_THREAD_ID"}, row["harness_markers"])
	assert.Equal(t, "skipped_no_node", row["board_line"])
	encoded, err := json.Marshal(row)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "synthetic-thread-value-not-for-log")
	diagnostic := doctorCLIExecute(t, root, "", "doctor", "--json")
	assert.Equal(t, 0, diagnostic.exit, diagnostic.stderr)
	permissionDraftAssertConfirmationRow(t, diagnostic.stdout, row)
}

func TestAgentDeny_NoTTY_AllowedAndMissingIdentityMinted(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionDraftRegister(t, root, "permission-fixture")
	_, err := os.Stat(filepath.Join(root, ".mtix", "board-id"))
	require.True(t, os.IsNotExist(err))
	out := doctorCLIExecute(t, root, "", "agent", "deny", "permission-fixture")
	require.Equal(t, 0, out.exit, out.stderr)
	rows := permissionDraftLog(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "deny", rows[0]["action"])
	assert.Equal(t, permissionDraftIdentity(t, root), rows[0]["board_id"])
	assert.Equal(t, "skipped_no_node", rows[0]["board_line"])
	data := doctorCLIExecute(t, root, "", "export")
	require.Equal(t, 0, data.exit, data.stderr)
	var exported struct {
		Nodes []any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(data.stdout), &exported))
	assert.Empty(t, exported.Nodes, "empty-board deny must not create a carrier node")
}

func TestConfirmationBoardLine_NeutralNoHostAgentPathOrMarkerValues(t *testing.T) {
	const neutral = "wake permissions changed on a host (see that host's mtix doctor)"
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	t.Setenv("CODEX_THREAD_ID", "synthetic-thread-value-not-for-log")
	created := doctorCLIExecute(t, root, "", "create", "Carrier fixture")
	require.Equal(t, 0, created.exit, created.stderr)
	permissionDraftRegister(t, root, "permission-fixture")
	out := doctorCLIExecute(t, root, "", "agent", "allow", "permission-fixture", "--yes")
	require.Equal(t, 0, out.exit, out.stderr)
	result := doctorCLIExecute(t, root, "", "export")
	require.Equal(t, 0, result.exit, result.stderr)
	var data struct {
		Nodes []struct {
			ID          string `json:"id"`
			Annotations []struct {
				Author string `json:"author"`
				Text   string `json:"text"`
				To     string `json:"addressee"`
			} `json:"annotations"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.stdout), &data))
	require.Len(t, data.Nodes, 1)
	assert.Equal(t, "TEST-1", data.Nodes[0].ID)
	require.Len(t, data.Nodes[0].Annotations, 1, "one CLI permission batch emits exactly one line")
	line := data.Nodes[0].Annotations[0]
	assert.Equal(t, neutral, line.Text)
	assert.Equal(t, "mtix-permissions", line.Author)
	assert.Empty(t, line.To)
	for _, forbidden := range []string{"permission-fixture", "doctor-fixture", root, "synthetic-thread-value-not-for-log"} {
		assert.NotContains(t, line.Text+line.Author+line.To, forbidden)
	}
}
