// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type relayQRow struct {
	Handle string `json:"handle"`
	Source string `json:"source"`
	NodeID string `json:"node_id"`
	OpType string `json:"op_type"`
	Relay  *struct {
		PeerID      string `json:"peer_id"`
		RS          uint64 `json:"rs"`
		State       string `json:"state"`
		Resolution  string `json:"resolution"`
		RecreatedAs string `json:"recreated_as"`
		Payload     string `json:"payload"`
	} `json:"relay"`
}

// relayQuarantineRows lists the quarantine as JSON and keeps the relay rows.
func relayQuarantineRows(t *testing.T, dir string) []relayQRow {
	t.Helper()
	var all []relayQRow
	require.NoError(t, json.Unmarshal([]byte(mustCLI(t, dir, "sync", "quarantine", "list", "--json")), &all))
	var out []relayQRow
	for _, r := range all {
		if r.Source == "relay" {
			out = append(out, r)
		}
	}
	return out
}

type relayQDoctorRow struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
	Fix  string `json:"fix"`
	Det  string `json:"detail"`
}

func relayDoctorRow(t *testing.T, dir string) (relayQDoctorRow, bool) {
	t.Helper()
	out, _, _ := cliIn(t, dir, "sync", "doctor", "--json")
	var rep struct {
		Checks []relayQDoctorRow `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out[strings.Index(out, "{"):]), &rep), out)
	for _, c := range rep.Checks {
		if c.Name == "relay quarantined records" {
			return c, true
		}
	}
	return relayQDoctorRow{}, false
}

// collidedProject returns a project whose relay tick skipped the other peer's
// colliding create (both peers took the next task number).
func collidedProject(t *testing.T) string {
	t.Helper()
	identityOperatorState(t)
	oldDir, _, _ := rejoinFixture(t, true, false)
	out := mustCLI(t, oldDir, "sync", "relay", "tick")
	require.Contains(t, out, "quarantined 1 record(s)")
	return oldDir
}

// TestRelayQuarantineCLI_Collision_IsListedAndDoctored is the MTIX-268 end to
// end: each mtix call is a new process, so the row is read back after a restart.
func TestRelayQuarantineCLI_Collision_IsListedAndDoctored(t *testing.T) {
	dir := collidedProject(t)

	rows := relayQuarantineRows(t, dir)
	require.Len(t, rows, 1)
	require.Equal(t, "create_node", rows[0].OpType)
	require.Equal(t, "open", rows[0].Relay.State)
	require.Contains(t, rows[0].Relay.Payload, "Other peer work", "the skipped record's content is kept")
	require.Contains(t, mustCLI(t, dir, "sync", "quarantine", "list"), "relay "+rows[0].Relay.PeerID)

	row, ok := relayDoctorRow(t, dir)
	require.True(t, ok, "doctor reports the skipped record")
	require.False(t, row.Pass)
	require.Contains(t, row.Fix, "mtix sync quarantine resolve --handle "+rows[0].Handle+" --recreate --reason")

	// The top-level doctor carries the same row, with its fix, in --json.
	top, _, _ := cliIn(t, dir, "doctor", "--json")
	require.Contains(t, top, `"id":"sync.relay-quarantined-records","status":"fail"`)
	require.Contains(t, top, "mtix sync quarantine resolve --handle "+rows[0].Handle)
}

// TestRelayQuarantineCLI_Accept_KeepsContentUnappliedAndClearsDoctor.
func TestRelayQuarantineCLI_Accept_KeepsContentUnappliedAndClearsDoctor(t *testing.T) {
	dir := collidedProject(t)
	h := relayQuarantineRows(t, dir)[0].Handle

	out := mustCLI(t, dir, "sync", "quarantine", "resolve", "--handle", h, "--accept", "--reason", "reviewed, duplicate work")
	require.Contains(t, out, "stays stored and unapplied")
	require.Contains(t, out, "mtix sync quarantine list --json")
	require.Contains(t, out, "only in this project's store, so keep it")
	require.NotContains(t, mustCLI(t, dir, "list", "--json"), "Other peer work")

	rows := relayQuarantineRows(t, dir)
	require.Equal(t, "accepted", rows[0].Relay.Resolution)
	require.Contains(t, rows[0].Relay.Payload, "Other peer work", "settled rows keep their content")
	row, _ := relayDoctorRow(t, dir)
	require.True(t, row.Pass)

	_, errOut, err := cliIn(t, dir, "sync", "quarantine", "resolve", "--handle", h, "--accept", "--reason", "again")
	require.Error(t, err)
	require.Contains(t, errOut+err.Error(), "already resolved")
}

// TestRelayQuarantineCLI_Recreate_MakesTheTaskUnderANewID.
func TestRelayQuarantineCLI_Recreate_MakesTheTaskUnderANewID(t *testing.T) {
	dir := collidedProject(t)
	h := relayQuarantineRows(t, dir)[0].Handle

	out := mustCLI(t, dir, "sync", "quarantine", "resolve", "--handle", h, "--recreate", "--reason", "keep the other peer's task")
	require.Contains(t, out, "recreated relay record")
	require.Contains(t, out, "which still holds the original and so holds both copies")
	require.Contains(t, out, `mtix cancel <id> --reason "duplicate of <other-id>"`)
	rows := relayQuarantineRows(t, dir)
	require.Equal(t, "recreated", rows[0].Relay.Resolution)
	newID := rows[0].Relay.RecreatedAs
	require.NotEmpty(t, newID)
	require.NotEqual(t, rows[0].NodeID, newID, "a new id, not the colliding one")
	require.Contains(t, mustCLI(t, dir, "show", newID), "Other peer work")
	require.Contains(t, mustCLI(t, dir, "list", "--json"), "Old peer same number", "the local task is untouched")
}

// TestRelayQuarantineCLI_Resolve_RejectsBadInput pins the exact refusals.
func TestRelayQuarantineCLI_Resolve_RejectsBadInput(t *testing.T) {
	dir := collidedProject(t)
	h := relayQuarantineRows(t, dir)[0].Handle
	for name, args := range map[string][]string{
		"both modes":   {"--handle", h, "--accept", "--recreate", "--reason", "x"},
		"no mode":      {"--handle", h, "--reason", "x"},
		"blank reason": {"--handle", h, "--accept", "--reason", "  "},
		"short handle": {"--handle", "abc", "--accept", "--reason", "x"},
		"no such":      {"--handle", "00000000", "--accept", "--reason", "x"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := cliIn(t, dir, append([]string{"sync", "quarantine", "resolve"}, args...)...)
			require.Error(t, err)
		})
	}
	require.Equal(t, "open", relayQuarantineRows(t, dir)[0].Relay.State, "a refused resolve changes nothing")
}

// TestRelayQuarantineCLI_ResolveHelp_StatesTheDuplicateAndTheCancel pins the
// help text: a recreate reaches the author, who holds both copies.
func TestRelayQuarantineCLI_ResolveHelp_StatesTheDuplicateAndTheCancel(t *testing.T) {
	long := newSyncQuarantineResolveCmd().Long
	for _, w := range []string{"EVERY peer, including the author peer", "BOTH copies", `mtix cancel <id> --reason "duplicate of <other-id>"`,
		"recreated_as", "only in this project's store, so keep that store", "when the author peer still holds the task",
		"resolve the parent first", "ambiguous when\nanother peer also had a create", "already recreated"} {
		require.Contains(t, long, w)
	}
}

// TestRelayQuarantineDocs_StateTheDuplicateAndTheCancel: every shipped place that
// offers a recreate says that the author peer then holds both copies and how to
// close one (MTIX-268).
func TestRelayQuarantineDocs_StateTheDuplicateAndTheCancel(t *testing.T) {
	for _, rel := range []string{
		"internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
		"USERMANUAL.md", "internal/docs/templates/troubleshooting.md.tmpl", "docs/TROUBLESHOOTING.md",
		"internal/docs/templates/claude.md.tmpl", "internal/docs/templates/agents.md.tmpl",
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		require.NoError(t, err, rel)
		text := string(raw)
		require.Contains(t, text, "both copies", rel)
		require.Contains(t, text, `mtix cancel <id> --reason "duplicate of <other-id>"`, rel)
	}
}
