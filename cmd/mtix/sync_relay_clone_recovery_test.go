// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// cliIn runs one real mtix command in a child process with dir as the
// working directory and returns stdout and stderr.
func cliIn(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestHelperProcessMtixCLI$") //nolint:gosec // re-executes this test binary
	child.Dir = dir
	child.Env = append(hostguard.Environ(), "MTIX_TEST_HELPER_PROCESS=1",
		"MTIX_TEST_HELPER_ARGV="+strings.Join(args, "\x1f"), "MTIX_SYNC_DSN=")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	err := child.Run()
	return stdout.String(), stderr.String(), err
}

func mustCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, errOut, err := cliIn(t, dir, args...)
	require.NoError(t, err, "mtix %s\nstdout=%s\nstderr=%s", strings.Join(args, " "), out, errOut)
	return out
}

const (
	rejoinOldPeer = diagnosticSelf
	rejoinNewPeer = "2123456789abcdef-new"
	rejoinOther   = "1123456789abcdef"
)

// copyRelayKeys copies every key epoch file of one project into another with
// owner-only modes, as the recovery step asks (never printed).
func copyRelayKeys(t *testing.T, from, to string) {
	t.Helper()
	keys, err := filepath.Glob(filepath.Join(from, ".mtix", "relay", "keys", "*"))
	require.NoError(t, err)
	require.NotEmpty(t, keys)
	dest := filepath.Join(to, ".mtix", "relay", "keys")
	require.NoError(t, os.MkdirAll(dest, 0o700))
	require.NoError(t, os.Chmod(dest, 0o700))
	for _, k := range keys {
		raw, readErr := os.ReadFile(k) // #nosec G304 -- test-owned path
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(dest, filepath.Base(k)), raw, 0o600))
	}
}

var publishedRe = regexp.MustCompile(`published (\d+)`)

// publishedCount runs a tick and returns how many events it published,
// failing on any divergence report.
func publishedCount(t *testing.T, dir string) int {
	t.Helper()
	out, errOut, err := cliIn(t, dir, "sync", "relay", "tick")
	require.NoError(t, err, "stderr=%s", errOut)
	require.NotContains(t, out+errOut, "RELAY_PUBLISHER_DIVERGED")
	m := publishedRe.FindStringSubmatch(out)
	require.NotNil(t, m, "tick output=%s", out)
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n
}

// rejoinFixture builds a relay shared by the original peer and another peer.
// The original holds work it has not published, and the other peer holds work
// the original ingested. It returns the original and other project dirs and
// the relay dir.
func rejoinFixture(t *testing.T, collide, earlyTick bool) (oldDir, otherDir, relay string) {
	t.Helper()
	oldDir, otherDir, relay = t.TempDir(), t.TempDir(), t.TempDir()
	mustCLI(t, oldDir, "init", "TEST")
	mustCLI(t, oldDir, "create", "Original first")
	mustCLI(t, oldDir, "config", "set", "sync.relay.peer_id", rejoinOldPeer)
	mustCLI(t, oldDir, "sync", "relay", "init", relay)
	mustCLI(t, oldDir, "config", "set", "sync.relay.dir", relay)
	mustCLI(t, oldDir, "sync", "relay", "tick")
	mustCLI(t, oldDir, "sync", "relay", "clone", "--export")

	mustCLI(t, otherDir, "init", "TEST")
	mustCLI(t, otherDir, "config", "set", "sync.relay.peer_id", rejoinOther)
	copyRelayKeys(t, oldDir, otherDir)
	mustCLI(t, otherDir, "config", "set", "sync.relay.dir", relay)
	mustCLI(t, otherDir, "sync", "relay", "clone")
	mustCLI(t, otherDir, "create", "Other peer work")
	if collide {
		// Both peers take the next task number before either ingests the other.
		mustCLI(t, oldDir, "create", "Old peer same number")
	}
	mustCLI(t, otherDir, "sync", "relay", "tick")

	if !collide || earlyTick {
		// The original ingests the other peer's work. With a collision it does
		// not tick here, so the tick of recovery step 1 is the one that skips
		// the colliding record and prints the quarantined line.
		mustCLI(t, oldDir, "sync", "relay", "tick")
	}
	mustCLI(t, oldDir, "create", "Original last, not yet published")

	return oldDir, otherDir, relay
}

// runRejoinStep runs one step of the printed recovery for real.
func runRejoinStep(t *testing.T, step sqlite.RelayRejoinStep, oldDir, newDir, relay string) string {
	t.Helper()
	dir := oldDir
	if step.InNewProject {
		dir = newDir
	}
	if step.Command == "" {
		copyRelayKeys(t, oldDir, newDir)
		return ""
	}
	cmdline := strings.NewReplacer("<PREFIX>", "TEST", "<new-peer-id>", rejoinNewPeer,
		"<dir>", relay, "<old-peer-id>", rejoinOldPeer).Replace(step.Command)
	fields := strings.Fields(cmdline)
	require.Equal(t, "mtix", fields[0], "every step command is an mtix command")
	return mustCLI(t, dir, fields[1:]...)
}

// TestRelayClone_RecoveryNamedByRefusal_RejoinsAsNewPeer runs the recovery
// steps taken from the refusal's own step list, through real commands in two
// real project directories. The relay holds another peer's work and the
// original peer's own last unpublished work. It checks that every step is
// printed in the refusal, that the new project holds both peers' work,
// publishes under its new id with no divergence, reaches the other peer, and
// that the original project is untouched by the new one.
func TestRelayClone_RecoveryNamedByRefusal_RejoinsAsNewPeer(t *testing.T) {
	identityOperatorState(t)
	oldDir, otherDir, relay := rejoinFixture(t, false, false)
	newDir := t.TempDir()

	_, errOut, err := cliIn(t, oldDir, "sync", "relay", "clone")
	require.Error(t, err)
	require.Contains(t, errOut, sqlite.ErrRelayCloneOnSyncedStore.Error())
	require.Contains(t, errOut, "nothing was imported")
	steps := sqlite.RelayCloneOnSyncedSteps()
	requireRejoinContract(t, steps)
	for i, step := range steps {
		require.Contains(t, errOut, step.Text, "the refusal prints step %d", i+1)
	}
	listBefore := mustCLI(t, oldDir, "list", "--json")

	// The first snapshot is pruned once every stamped peer has read past it,
	// so the recovery has to export a fresh one.
	snaps, err := filepath.Glob(filepath.Join(relay, "bootstrap", "*"))
	require.NoError(t, err)
	for _, snap := range snaps {
		require.NoError(t, os.Remove(snap))
	}

	var statusAfterOldSide string
	for _, step := range steps {
		if step.InNewProject && statusAfterOldSide == "" {
			statusAfterOldSide = mustCLI(t, oldDir, "sync", "relay", "status")
		}
		out := runRejoinStep(t, step, oldDir, newDir, relay)
		if step.Command == "mtix sync relay tick" && !step.InNewProject {
			assert.NotContains(t, out, "quarantined", "no collision, so the first check passes")
		}
		if step.Command == sqlite.RelayCloneOnSyncedListCommand {
			assert.Empty(t, missingUIDs(t, runRejoinStep(t, step, oldDir, oldDir, relay), out), "the second check passes: nothing is missing by uid")
		}
	}
	assertDoctorBoardRowOK(t, newDir)

	list := mustCLI(t, newDir, "list", "--json")
	for _, title := range []string{"Original first", "Other peer work", "Original last, not yet published"} {
		assert.Contains(t, list, title, "the new project holds %q", title)
	}
	status := mustCLI(t, newDir, "sync", "relay", "status")
	assert.Contains(t, status, "this peer "+rejoinNewPeer, "the new project joined under a new peer id")
	assert.Contains(t, status, rejoinOldPeer+"  ingested", "the new project follows the old peer's stream")
	assert.Contains(t, status, "RETIRED", "the old peer id is retired")

	mustCLI(t, newDir, "create", "New project work")
	assert.Positive(t, publishedCount(t, newDir), "a tick in the new project publishes")
	mustCLI(t, otherDir, "sync", "relay", "tick")
	otherList := mustCLI(t, otherDir, "list", "--json")
	assert.Contains(t, otherList, "New project work", "the other peer receives the new project's work")
	assert.Contains(t, otherList, "Original last, not yet published", "the retired peer's last work reached the other peer")

	assert.Equal(t, listBefore, mustCLI(t, oldDir, "list", "--json"), "the original project is untouched")
	assert.Equal(t, thisPeerLine(t, statusAfterOldSide), thisPeerLine(t, mustCLI(t, oldDir, "sync", "relay", "status")),
		"the original's own publisher state is untouched (the shared peer list gains the new peer)")
}

// TestRelayCloneOnSyncedSteps_Docs_NameEveryCommand verifies the shipped
// skill template names every command of the recovery, so the docs cannot
// drift from the steps the tests run.
func TestRelayCloneOnSyncedSteps_Docs_NameEveryCommand(t *testing.T) {
	skill, err := os.ReadFile(filepath.Join("..", "..", "internal", "docs", "templates", "skills", "sync.md.tmpl"))
	require.NoError(t, err)
	for _, step := range sqlite.RelayCloneOnSyncedSteps() {
		if step.Command == "" {
			continue
		}
		cmd := step.Command
		if i := strings.Index(cmd, " <"); i >= 0 {
			cmd = cmd[:i]
		}
		assert.Contains(t, string(skill), cmd, "the sync skill names %q", cmd)
	}
}

// thisPeerLine returns the "this peer" line of a relay status report.
func thisPeerLine(t *testing.T, status string) string {
	t.Helper()
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "this peer ") {
			return line
		}
	}
	require.FailNow(t, "no this-peer line", status)
	return ""
}

// requireRejoinContract pins what the recovery must contain. Some steps are
// redundant while the relay still holds the whole history (a clone, the
// first tick in the new project), so running the steps alone cannot prove
// they are listed; a pruned relay needs them.
func requireRejoinContract(t *testing.T, steps []sqlite.RelayRejoinStep) {
	t.Helper()
	have := map[string]sqlite.RelayRejoinStep{}
	keyCopy := 0
	for _, step := range steps {
		have[step.Command] = step
		if step.Command == "" {
			keyCopy++
			assert.Contains(t, step.Text, "0700")
			assert.Contains(t, step.Text, "0600")
			assert.Contains(t, step.Text, "never print")
		}
	}
	assert.Equal(t, 1, keyCopy, "exactly one key copy step")
	for _, want := range []string{
		"mtix sync relay tick", "mtix sync relay clone --export", "mtix init <PREFIX>",
		"mtix config set sync.relay.peer_id <new-peer-id>", "mtix config set sync.relay.dir <dir>",
		"mtix sync relay clone", "mtix sync relay retire-peer <old-peer-id>",
	} {
		assert.Contains(t, have, want, "the recovery lists %q", want)
	}
	assert.Contains(t, have["mtix sync relay clone"].Text, "stop any daemon there")
	assert.NotContains(t, have[sqlite.RelayCloneOnSyncedListCommand].Text, "mtix create", "no recreate step: it duplicates tasks")
	assert.NotContains(t, have[sqlite.RelayCloneOnSyncedListCommand].Text, "recreate")
	assert.Contains(t, have["mtix sync relay retire-peer <old-peer-id>"].Text, "only when the checks of steps 1 and 9 passed")
	requireStepWords(t, steps)
	id := have["mtix config set sync.relay.peer_id <new-peer-id>"]
	assert.True(t, id.InNewProject)
	assert.Contains(t, id.Text, "NEW peer id")
	assert.Contains(t, id.Text, "never reuse")
	assert.Contains(t, id.Text, "RELAY_PUBLISHER_DIVERGED")
	assert.Contains(t, have["mtix sync relay clone"].Text, "before any `mtix sync relay attach`")
	ticks := 0
	for _, step := range steps {
		if step.Command == "mtix sync relay tick" && step.InNewProject {
			ticks++
		}
	}
	assert.Equal(t, 1, ticks, "the new project ticks once after the clone")
	for _, step := range steps {
		if step.Command == "mtix sync relay tick" && !step.InNewProject {
			assert.Contains(t, step.Text, sqlite.RelayCloneOnSyncedQuarantineLine, "the old project's tick output is to be read")
		}
		if step.Command == "mtix sync relay tick" && step.InNewProject {
			assert.Contains(t, step.Text, "stop the daemon in the old project", "the old daemon stops before the final tick")
			assert.Contains(t, step.Text, "from the start", "the replay claim is accurate")
			assert.NotContains(t, step.Text, "past the snapshot")
		}
	}
}

// parseListUIDs parses the output of the step 9 list command and returns its
// uids. It fails unless the number of rows read equals the reported total,
// which is the truncation check the step demands.
func parseListUIDs(list string) ([]string, error) {
	var doc struct {
		Nodes []struct {
			UID string `json:"uid"`
		} `json:"nodes"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(list), &doc); err != nil {
		return nil, fmt.Errorf("parse list: %w", err)
	}
	if doc.Total != len(doc.Nodes) {
		return nil, fmt.Errorf("read %d rows of a total of %d: truncated", len(doc.Nodes), doc.Total)
	}
	uids := make([]string, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		if n.UID == "" {
			return nil, fmt.Errorf("a row has no uid field")
		}
		uids = append(uids, n.UID)
	}
	return uids, nil
}

func listUIDs(t *testing.T, list string) []string {
	t.Helper()
	uids, err := parseListUIDs(list)
	require.NoError(t, err, list)
	require.NotEmpty(t, uids)
	return uids
}

// missingUIDs plays the operator of the verify step: the uids the old
// project holds that the new project's list lacks. It compares by uid only.
func missingUIDs(t *testing.T, oldList, newList string) []string {
	t.Helper()
	have := map[string]bool{}
	for _, uid := range listUIDs(t, newList) {
		have[uid] = true
	}
	var missing []string
	for _, uid := range listUIDs(t, oldList) {
		if !have[uid] {
			missing = append(missing, uid)
		}
	}
	return missing
}

// assertDoctorBoardRowOK checks the doctor row a collision turned red
// (reviewer S2): on the recovery's normal path it stays green.
func assertDoctorBoardRowOK(t *testing.T, dir string) {
	t.Helper()
	out, _, _ := cliIn(t, dir, "sync", "doctor")
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "board matches sync log") {
			found = true
			assert.NotContains(t, line, "FAIL", "doctor board row on the normal path")
		}
	}
	assert.True(t, found, "doctor prints the board row: %s", out)
}

// TestRelayClone_RecoveryWithTaskNumberCollision_StopsAndRetiresNothing covers
// two peers that took the same task number: the old project skips the other
// peer's task at ingest, keeps it nowhere, and the export stamps that stream
// past it. The recovery fails closed: the tick of step 1 prints the
// quarantined line, the operator stops with the exact STOP prose, no step
// after it runs (so retire-peer never runs), and both tasks are still held
// by their own peers.
func TestRelayClone_RecoveryWithTaskNumberCollision_StopsAndRetiresNothing(t *testing.T) {
	identityOperatorState(t)
	oldDir, otherDir, relay := rejoinFixture(t, true, false)
	newDir := t.TempDir()
	listBefore := mustCLI(t, oldDir, "list", "--json")
	require.NotContains(t, listBefore, "Other peer work", "the old project holds its own task only")

	stopped := false
	ran := 0
	for _, step := range sqlite.RelayCloneOnSyncedSteps() {
		var out, errOut string
		if step.Command == "mtix sync relay tick" && !step.InNewProject {
			var err error
			out, errOut, err = cliIn(t, oldDir, "sync", "relay", "tick")
			require.NoError(t, err)
		} else {
			out = runRejoinStep(t, step, oldDir, newDir, relay)
		}
		ran++
		if step.Command == "mtix sync relay tick" && !step.InNewProject {
			require.Contains(t, errOut, "relay ingest quarantined a record", "the log line the step's log search looks for is real")
			require.Contains(t, out, "quarantined 1 record(s) from authenticated peers:", "the colliding record is skipped at this tick")
			require.Contains(t, step.Text, sqlite.RelayCloneOnSyncedQuarantineLine)
			require.Contains(t, step.Text, sqlite.RelayCloneOnSyncedStop)
			stopped = true
			break
		}
	}
	require.True(t, stopped)
	assert.Equal(t, 1, ran, "the recovery stops at step 1")
	assert.Equal(t, "STOP, do not continue: keep the old project, do not run `mtix sync relay retire-peer`, "+
		"and ask the project maintainer. Records the old project skipped are kept in no store, and the durable fix is MTIX-268",
		sqlite.RelayCloneOnSyncedStop)

	status := mustCLI(t, oldDir, "sync", "relay", "status")
	assert.NotContains(t, status, "RETIRED", "no peer id was retired")
	assert.Contains(t, mustCLI(t, oldDir, "list", "--json"), "Old peer same number", "the old project still holds its task")
	assert.Contains(t, mustCLI(t, oldDir, "list", "--json"), "Original first")
	assert.Contains(t, mustCLI(t, otherDir, "list", "--json"), "Other peer work", "the other peer still holds its task")
	entries, err := os.ReadDir(newDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the new project was never created")
}

// TestRelayCloneOnSyncedLogSearch_FindsOnlyARecordedSkip runs the step 1 log
// search for real: it prints the line a supervised daemon captured and is
// silent when there is no log.
func TestRelayCloneOnSyncedLogSearch_FindsOnlyARecordedSkip(t *testing.T) {
	dir := t.TempDir()
	run := func() string {
		c := exec.Command("sh", "-c", sqlite.RelayCloneOnSyncedLogSearch) //nolint:gosec // constant command under test
		c.Dir = dir
		out, _ := c.CombinedOutput()
		return string(out)
	}
	assert.Empty(t, run(), "no daemon log, no output, no error text")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".mtix", "logs"), 0o700))
	logFile := filepath.Join(dir, ".mtix", "logs", "daemon.err.log")
	require.NoError(t, os.WriteFile(logFile, []byte("level=INFO msg=ok\n"), 0o600))
	assert.Empty(t, run(), "a log without a skip prints nothing")
	require.NoError(t, os.WriteFile(logFile, []byte("level=ERROR msg=\"relay ingest quarantined a record from an authenticated peer\" peer=x\n"), 0o600))
	assert.Contains(t, run(), "relay ingest quarantined a record")
}

// TestRelayClone_SkipFromEarlierTick_IsNotDetectedAndSaysSo pins the known
// gap (MTIX-268): a skip made by an earlier interactive tick leaves neither
// the tick line nor a log, so step 1 shows nothing, and the step text says
// plainly that this cannot be detected and that step 9 does not catch it.
func TestRelayClone_SkipFromEarlierTick_IsNotDetectedAndSaysSo(t *testing.T) {
	identityOperatorState(t)
	oldDir, _, _ := rejoinFixture(t, true, true)
	step := sqlite.RelayCloneOnSyncedSteps()[0]
	out := mustCLI(t, oldDir, "sync", "relay", "tick")
	assert.NotContains(t, out, "quarantined", "the earlier tick already skipped the record")
	assert.Contains(t, step.Text, "cannot be detected until MTIX-268 lands")
	assert.Contains(t, step.Text, "step 9 does not catch it")
	_, err := os.Stat(filepath.Join(oldDir, ".mtix", "logs", "daemon.err.log"))
	assert.True(t, os.IsNotExist(err), "an interactive tick leaves no log")
}

// TestRelayClone_VerifyStepCoversEveryTaskInEveryProject builds a board of
// more than 50 tasks in two projects, runs the recovery up to the verify step
// and removes from the new project one task that sits past row 50. The bare
// list the earlier steps used (50 rows, primary project) cannot see it;
// the step's command does, and its row count equals the total.
func TestRelayClone_VerifyStepCoversEveryTaskInEveryProject(t *testing.T) {
	identityOperatorState(t)
	oldDir, _, relay := rejoinFixture(t, false, false)
	for i := 0; i < 52; i++ {
		mustCLI(t, oldDir, "create", fmt.Sprintf("Bulk %d", i))
	}
	mustCLI(t, oldDir, "create", "Second project task", "--project", "OTHER", "--yes")
	newDir := t.TempDir()
	steps := sqlite.RelayCloneOnSyncedSteps()
	require.NoError(t, os.RemoveAll(filepath.Join(relay, "bootstrap")))
	var newList string
	for _, step := range steps[:9] {
		out := runRejoinStep(t, step, oldDir, newDir, relay)
		if step.Command == sqlite.RelayCloneOnSyncedListCommand {
			newList = out
		}
	}
	oldList := mustCLI(t, oldDir, "list", "--json", "--all-projects", "--limit", "100000", "--fields", "uid")
	require.Greater(t, len(listUIDs(t, oldList)), 55, "the board is past the default 50 rows and spans two projects")
	assert.Equal(t, len(listUIDs(t, oldList)), len(listUIDs(t, newList)))
	assert.Empty(t, missingUIDs(t, oldList, newList))

	// Lose the last-created task of the second project in the new project.
	mustCLI(t, newDir, "delete", "OTHER-1")
	newList = mustCLI(t, newDir, "list", "--json", "--all-projects", "--limit", "100000", "--fields", "uid")
	assert.Len(t, missingUIDs(t, oldList, newList), 1, "the step's command finds the lost task")
	bare := mustCLI(t, newDir, "list", "--json")
	bareOld := mustCLI(t, oldDir, "list", "--json")
	_, bareErr := parseListUIDs(bareOld)
	assert.Error(t, bareErr, "the bare list is truncated at 50 rows: its count check fails")
	assert.Contains(t, bare, `"total"`)
}

// TestMissingUIDs_NewProjectLacksTask_ReportsIt pins the second check: a task
// of the old project absent from the new project is found by uid even when
// its id and title are held by another task.
func TestMissingUIDs_NewProjectLacksTask_ReportsIt(t *testing.T) {
	oldList := `{"nodes":[{"uid":"u-old"}],"total":1}`
	newList := `{"nodes":[{"uid":"u-other"}],"total":1}`
	assert.Equal(t, []string{"u-old"}, missingUIDs(t, oldList, newList))
	assert.Empty(t, missingUIDs(t, oldList, oldList))
}

// TestListUIDs_TruncatedRead_FailsTheCount verifies that a read with fewer
// rows than the reported total is rejected, so truncation cannot pass.
func TestListUIDs_TruncatedRead_FailsTheCount(t *testing.T) {
	_, err := parseListUIDs(`{"nodes":[{"uid":"a"}],"total":2}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncated")
	_, err = parseListUIDs(`{"nodes":[{"id":"x"}],"total":1}`)
	require.Error(t, err, "a row without a uid is rejected")
}

// requireStepWords pins the key words of every step by index, so a reworded,
// reordered or renumbered step fails here. Step 9 and 10 name "steps 1 and 9"
// by number, so the indexes are asserted too.
func requireStepWords(t *testing.T, steps []sqlite.RelayRejoinStep) {
	t.Helper()
	words := [][]string{
		{"in this project", "read its output", "STOP", "keep the old project", "do not run `mtix sync relay retire-peer`",
			"ask the project maintainer", "MTIX-268", "only on the tick that skipped", "persisted nowhere (MTIX-268)",
			sqlite.RelayCloneOnSyncedLogSearch, "STOP the same way", "earlier interactive tick leaves no trace",
			"cannot be detected until MTIX-268 lands", "step 9 does not catch it"},
		{"in this project", "includes that work"},
		{"in a new project", "with the same prefix as this project"},
		{"NEW peer id", "never reuse"},
		{"out of band", "never through the relay", "0700", "0600", "never print", ".mtix/relay/keys"},
		{"there"},
		{"stop any daemon there", "before any `mtix sync relay attach`", "refuses an empty project"},
		{"stop the daemon in the old project too", "retired id", "from the start", "replaying it is harmless"},
		{"verify there with `" + sqlite.RelayCloneOnSyncedListCommand + "`", "in the old project", "equal its `total`", "truncated",
			"`uid` field", "never by id or title", "restart the daemon in the old project", "keep using the old project",
			"leave the new project unused (delete nothing)", "do not run `mtix sync relay retire-peer`", "ask the project maintainer",
			"the new project's peer id", "MTIX-268"},
		{"only when the checks of steps 1 and 9 passed", "prune window", "unset means the machine id", "stop using the old project"},
	}
	require.Len(t, steps, len(words))
	for i, ws := range words {
		for _, w := range ws {
			assert.Contains(t, steps[i].Text, w, "step %d names %q", i+1, w)
		}
	}
	assert.Equal(t, sqlite.RelayCloneOnSyncedListCommand, steps[8].Command, "step 9 is the uid check")
	assert.Contains(t, steps[8].Command, "--all-projects")
	assert.Contains(t, steps[8].Command, "--limit 100000")
	assert.Contains(t, steps[8].Command, "--fields uid")
	assert.Equal(t, "mtix sync relay tick", steps[0].Command, "step 1 is the quarantine check")
	assert.False(t, steps[0].InNewProject)
	assert.True(t, steps[8].InNewProject)
	ways := sqlite.RelayCloneOnSyncedWays()
	assert.True(t, strings.HasPrefix(ways, "To rejoin as a new peer (the old project is retired only by the last step, and only if every check passes): (1) "), ways)
	assert.Contains(t, ways, "; (10) ")
	assert.NotContains(t, ways, "(11)")
}

// TestRelayCloneCmd_Help_NamesNewPeerRecovery verifies the clone help says a
// synced store is refused and rejoins as a new peer.
func TestRelayCloneCmd_Help_NamesNewPeerRecovery(t *testing.T) {
	long := newRelayCloneCmd().Long
	assert.Contains(t, long, "is refused")
	assert.Contains(t, long, "nothing is written")
	assert.Contains(t, long, "NEW peer")
	assert.Contains(t, long, "sync.relay.peer_id")
}
