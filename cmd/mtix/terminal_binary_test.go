// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestTerminalBinary_HostileBoard_NoControls exercises the actual entry point,
// command wiring, writers, and error path; no command function is called in-process.
func TestTerminalBinary_HostileBoard_NoControls(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mtix")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build real binary: %s", output)
	initTestApp(t)
	seedTerminalBinaryBoard(t)
	for _, command := range terminalBinaryCommands(t) {
		t.Run(fmt.Sprint(command.args), func(t *testing.T) {
			runTerminalBinary(t, binary, command)
		})
	}
}

type terminalBinaryCommand struct {
	args        []string
	stdin, want string
	wantError   bool
	// wantIDs requires at least one hostile event id, rendered by
	// format.EventID, in the output.
	wantIDs bool
}

func terminalBinaryCommands(t *testing.T) []terminalBinaryCommand {
	t.Helper()
	var plan bytes.Buffer
	encoder := json.NewEncoder(&plan)
	for _, f := range testutil.HostileText() {
		if len(f.Value) > model.MaxTitleLength {
			continue
		}
		require.NoError(t, encoder.Encode(map[string]string{"title": f.Value, "description": f.Value}))
	}
	return []terminalBinaryCommand{
		{args: []string{"list", "--limit", "1000"}, want: "TEST-1"},
		{args: []string{"ready"}, want: "TEST-1.1"},
		{args: []string{"search", "--limit", "1000"}, want: "TEST-1"},
		{args: []string{"tree", "TEST-1"}, want: "TEST-1.1"},
		{args: []string{"progress", "TEST-1"}, want: "TEST-1"},
		{args: []string{"show", "TEST-1"}, want: "Hostile board"},
		{args: []string{"show", "TEST-1", "--history"}, want: "Hostile board"},
		{args: []string{"context", "TEST-1.1"}, want: "Context Chain"},
		{args: []string{"agent", "work", "terminal-reader"}, want: "Hostile board"},
		{args: []string{"decompose", "TEST-1", "--file", "-", "--dry-run"}, stdin: plan.String(), want: "TEST-1"},
		{args: []string{"sync", "status"}, want: "quarantined"},
		{args: []string{"sync", "quarantine", "list"}, want: "EVENT ID"},
		{args: []string{"sync", "doctor"}, want: "quarantined events", wantError: true, wantIDs: true},
		{args: []string{"sync", "quarantine", "acknowledge", "missing\x1b]0;evil\a", "--reason", "reviewed"}, want: "quarantined event: not found", wantError: true},
		{args: []string{"create", "Created\x1b]0;evil\a title\r\ntext", "--under", "TEST-1"}, want: "Created"},
	}
}

func runTerminalBinary(t *testing.T, binary string, command terminalBinaryCommand) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, command.args...)
	cmd.Dir = filepath.Dir(app.mtixDir)
	cmd.Env = append(os.Environ(), "MTIX_AUTHOR_ID=test")
	cmd.Stdin = strings.NewReader(command.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if command.wantError {
		require.Error(t, runErr, "diagnostic or missing-event failure must execute")
		var exit *exec.ExitError
		require.ErrorAs(t, runErr, &exit)
	} else {
		require.NoError(t, runErr, "stderr: %s", stderr.String())
	}
	require.NoError(t, ctx.Err(), "command timed out")
	require.Empty(t, binaryTerminalViolation(stdout.Bytes()), "stdout")
	require.Empty(t, binaryTerminalViolation(stderr.Bytes()), "stderr")
	require.Contains(t, stdout.String()+stderr.String(), command.want)
	if command.wantIDs {
		require.True(t, terminalHasRenderedID(stdout.String()+stderr.String()), "no rendered hostile event id in output")
	}
	if len(command.args) == 3 && command.args[1] == "quarantine" {
		for _, f := range testutil.HostileIDs() {
			require.Contains(t, stdout.String(), format.EventID(f.Value), f.Name)
		}
	}
}

func seedTerminalBinaryBoard(t *testing.T) {
	t.Helper()
	_, metaErr := app.store.WriteDB().ExecContext(t.Context(),
		`INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, "meta.sync.machine_hash", "0123456789abcdef")
	require.NoError(t, metaErr)
	parent, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", Title: "Hostile board\x1b[31m", Creator: "test"})
	require.NoError(t, err)
	var prose strings.Builder
	for _, fixture := range testutil.HostileText() {
		prose.WriteString(fixture.Value + "\n")
		seedTerminalBinaryChild(t, parent.ID, fixture)
	}
	_, err = app.store.WriteDB().ExecContext(t.Context(),
		`UPDATE nodes SET description = ?, prompt = ?, assignee = ? WHERE id = ?`, prose.String(), prose.String(), prose.String(), parent.ID)
	require.NoError(t, err)
	require.NoError(t, app.agentSvc.EnsureAgent(t.Context(), "terminal-reader", "TEST"))
	_, err = app.store.WriteDB().ExecContext(t.Context(),
		`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`, parent.ID, "terminal-reader")
	require.NoError(t, err)
	seedTerminalBinaryQuarantine(t)
}

func seedTerminalBinaryChild(t *testing.T, parentID string, fixture testutil.HostileFixture) {
	t.Helper()
	title := fixture.Value
	if len(title) > model.MaxTitleLength {
		title = "Long text in description"
	}
	child, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", ParentID: parentID, Title: title, Creator: "test"})
	require.NoError(t, err, fixture.Name)
	_, err = app.store.WriteDB().ExecContext(t.Context(),
		`UPDATE nodes SET description = ?, prompt = ?, acceptance = ? WHERE id = ?`, fixture.Value, fixture.Value, fixture.Value, child.ID)
	require.NoError(t, err)
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), parentID, fixture.Value, "test", ""))
}

func seedTerminalBinaryQuarantine(t *testing.T) {
	t.Helper()
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		for _, fixture := range testutil.HostileIDs() {
			var event model.SyncEvent
			require.NoError(t, json.Unmarshal([]byte(ackCLIRaw), &event))
			event.EventID = fixture.Value
			raw, err := json.Marshal(event)
			if err != nil {
				return err
			}
			if err := sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{
				EventID: fixture.Value, Source: "pull", RawEvent: string(raw),
				Reason: "hostile reason " + fixture.Value, FirstSeen: "2026-10-28T00:00:00Z", LastAttempt: "2026-10-28T00:00:00Z",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}

// Decode UTF-8 first: a continuation byte in printable Unicode is not a C1
// control. Invalid UTF-8 (including a bare 0x80..0x9f) is always refused.
func binaryTerminalViolation(out []byte) string {
	if !utf8.Valid(out) {
		return "invalid UTF-8 or raw C1 byte"
	}
	for _, r := range string(out) {
		if (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			return fmt.Sprintf("terminal control %U", r)
		}
	}
	return ""
}

func TestTerminalBinary_ControlScanner(t *testing.T) {
	for b := 0; b <= 0x9f; b++ {
		if (b >= 0x20 && b < 0x7f) || b == '\n' || b == '\t' {
			continue
		}
		require.NotEmpty(t, binaryTerminalViolation([]byte{byte(b)}), "byte %x", b)
		require.NotEmpty(t, binaryTerminalViolation([]byte(string(rune(b)))), "rune %x", b)
	}
	require.Empty(t, binaryTerminalViolation([]byte("Café 日本語 🙂\n\t")))
}

func terminalHasRenderedID(output string) bool {
	for _, f := range testutil.HostileIDs() {
		if strings.Contains(output, format.EventID(f.Value)) {
			return true
		}
	}
	return false
}
