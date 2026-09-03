// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// MTIX-90 acceptance: a piped-stdin run refuses; a wrong typed value
// aborts with no mutation; the correct value proceeds. Both gated
// commands are covered, and the piped case is also driven through the
// REAL entry point in a subprocess so the production stdin check is the
// one under test, not a stand-in.

// answerDestructive scripts the prompt: what the operator "types" and
// whether stdin looks like a terminal. Restored on cleanup.
func answerDestructive(t *testing.T, answer string, terminal bool) {
	t.Helper()
	origIn, origTTY := destructiveInput, destructiveStdinIsTerminal
	destructiveInput = strings.NewReader(answer + "\n")
	destructiveStdinIsTerminal = func() bool { return terminal }
	t.Cleanup(func() {
		destructiveInput, destructiveStdinIsTerminal = origIn, origTTY
	})
}

// typeTicketCount answers the prompt correctly at a simulated terminal:
// the exact number of rows in nodes, which is what the guard asks for.
func typeTicketCount(t *testing.T) {
	t.Helper()
	answerDestructive(t, strconv.Itoa(allNodeCount(t)), true)
}

// allNodeCount is every row in nodes, soft-deleted included — the count
// DELETE FROM nodes removes and the count the operator must type.
func allNodeCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM nodes`).Scan(&n))
	return n
}

func seedTickets(t *testing.T, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		_, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "TEST", Title: fmt.Sprintf("ticket %d", i+1), Creator: "u"})
		require.NoError(t, err)
	}
}

func preDestroySnapshots(t *testing.T, tag string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(app.mtixDir, "data", "backups", "pre-"+tag+"-*.db"))
	require.NoError(t, err)
	return matches
}

// --- the guard itself ---

func sampleOp() destructiveOp {
	return destructiveOp{
		Command:     "mtix sample --wipe",
		Scope:       "local store /tmp/x (projects: TEST)",
		Destroys:    []destroyCount{{Label: "tickets", N: 27}, {Label: "journal events", N: 140}},
		Consequence: "everything is gone",
		SnapshotTag: "sample",
	}
}

func TestConfirmDestructive_NonTerminalRefusesWithInstructions(t *testing.T) {
	answerDestructive(t, "27", false) // the right answer is on stdin, but stdin is a pipe
	var w bytes.Buffer
	err := confirmDestructive(&w, sampleOp())
	require.ErrorIs(t, err, errDestructiveRefused)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
	assert.Contains(t, err.Error(), "27 tickets, 140 journal events")
	assert.Contains(t, err.Error(), "no flag can supply it")
	assert.Contains(t, err.Error(), "Nothing was changed")
	// The warning is printed before the refusal so a CI log shows what
	// the command would have destroyed.
	assert.Contains(t, w.String(), "destroys: 27 tickets, 140 journal events")
	assert.Contains(t, w.String(), "--yes and --force do not bypass")
}

func TestConfirmDestructive_WrongValueAborts(t *testing.T) {
	for _, answer := range []string{"yes", "y", "", "28", "27 tickets"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			answerDestructive(t, answer, true)
			var w bytes.Buffer
			err := confirmDestructive(&w, sampleOp())
			require.ErrorIs(t, err, errDestructiveRefused)
			assert.Contains(t, err.Error(), `expected "27"`)
			assert.Contains(t, err.Error(), "nothing was changed")
		})
	}
}

func TestConfirmDestructive_ExactCountProceeds(t *testing.T) {
	answerDestructive(t, "  27 ", true) // surrounding whitespace is not a typo
	var w bytes.Buffer
	require.NoError(t, confirmDestructive(&w, sampleOp()))
	assert.Contains(t, w.String(), "WARNING: mtix sample --wipe is irreversible.")
	assert.Contains(t, w.String(), "scope:    local store /tmp/x (projects: TEST)")
	assert.Contains(t, w.String(), "Type the number of tickets to be destroyed (27)")
}

func TestConfirmDestructive_NothingToDestroySaysSoAndSkipsPrompt(t *testing.T) {
	answerDestructive(t, "", false) // no terminal, no answer — must still pass
	op := sampleOp()
	op.Destroys = []destroyCount{{Label: "tickets", N: 0}, {Label: "journal events", N: 0}}
	var w bytes.Buffer
	require.NoError(t, confirmDestructive(&w, op))
	assert.Contains(t, w.String(), "nothing to destroy (0 tickets, 0 journal events); no confirmation required")
}

func TestDestructiveOp_SnapshotPathAvoidsRollingBackupRotation(t *testing.T) {
	p := sampleOp().snapshotPath("/p/.mtix", mustTime(t, "2026-09-03T10:15:00Z"))
	assert.Equal(t, filepath.Join("/p/.mtix", "data", "backups", "pre-sample-20260903-101500.db"), p)
	// The scheduler rotates mtix-*.db; a pre-destroy snapshot must not match.
	assert.False(t, strings.HasPrefix(filepath.Base(p), "mtix-"))
}

// --- mtix sync reconcile --discard-local ---

func TestDiscardLocal_PipedStdinRefusesAndMutatesNothing(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 3)
	answerDestructive(t, "3", false) // even the right answer on a pipe is refused

	var stdout, stderr bytes.Buffer
	err := runSyncReconcile(context.Background(), &stdout, &stderr,
		reconcileFlags{discardLocal: true, yes: true})
	require.ErrorIs(t, err, errDestructiveRefused)
	assert.Contains(t, err.Error(), "mtix sync reconcile --discard-local")
	assert.Contains(t, err.Error(), "stdin is not a terminal")
	assert.Equal(t, 3, allNodeCount(t), "--yes alone must not wipe the store")
	assert.NotContains(t, stdout.String(), "discard-local complete")
	assert.Empty(t, preDestroySnapshots(t, "discard-local"), "no snapshot is taken for a refused run")
}

func TestDiscardLocal_WrongValueAbortsAndMutatesNothing(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 3)
	answerDestructive(t, "yes", true)

	var stdout, stderr bytes.Buffer
	err := runSyncReconcile(context.Background(), &stdout, &stderr,
		reconcileFlags{discardLocal: true, yes: true})
	require.ErrorIs(t, err, errDestructiveRefused)
	assert.Contains(t, err.Error(), `expected "3", got "yes"`)
	assert.Equal(t, 3, allNodeCount(t))
	assert.Empty(t, preDestroySnapshots(t, "discard-local"))
}

func TestDiscardLocal_ExactCountProceedsAfterSnapshot(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 3)
	typeTicketCount(t)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncReconcile(context.Background(), &stdout, &stderr,
		reconcileFlags{discardLocal: true, yes: true}))
	assert.Contains(t, stdout.String(), "discard-local complete")
	assert.Equal(t, 0, allNodeCount(t))

	// The warning named the operation, the scope and the exact counts.
	assert.Contains(t, stderr.String(), "WARNING: mtix sync reconcile --discard-local is irreversible.")
	assert.Contains(t, stderr.String(), "(projects: TEST)")
	assert.Contains(t, stderr.String(), "destroys: 3 tickets")
	assert.Contains(t, stderr.String(), "journal events")

	// And a verified snapshot exists, taken before the delete: it holds
	// the three tickets the live store no longer has.
	snaps := preDestroySnapshots(t, "discard-local")
	require.Len(t, snaps, 1)
	assert.Contains(t, stderr.String(), "snapshot: "+snaps[0])
	assert.Equal(t, 3, nodeCountInSnapshot(t, snaps[0]))
}

// TestDiscardLocal_RealPipedStdin_Refuses drives the actual CLI entry
// point in a child process with stdin connected to a pipe. No test hook
// is involved: the production os.Stdin check decides. This is the
// criterion-3 proof — `echo 3 | mtix sync reconcile --discard-local --yes`
// must fail closed and leave every ticket in place.
func TestDiscardLocal_RealPipedStdin_Refuses(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 3)
	projectDir := filepath.Dir(app.mtixDir)

	child := exec.Command(os.Args[0], "-test.run=^TestHelperProcessMtixCLI$") //nolint:gosec // re-executes this test binary
	child.Dir = projectDir
	child.Env = append(os.Environ(),
		"MTIX_TEST_HELPER_PROCESS=1",
		"MTIX_TEST_HELPER_ARGS=sync reconcile --discard-local --yes",
	)
	child.Stdin = strings.NewReader("3\n") // the right answer, but on a pipe
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr

	err := child.Run()
	require.Error(t, err, "the child must exit non-zero; stdout=%s stderr=%s", stdout.String(), stderr.String())
	assert.Contains(t, stderr.String(), "stdin is not a terminal")
	assert.Contains(t, stderr.String(), "destroys: 3 tickets")
	assert.NotContains(t, stdout.String(), "discard-local complete")
	assert.Equal(t, 3, allNodeCount(t), "a piped run must not touch the store")
}

// TestHelperProcessMtixCLI is the child side of the subprocess tests. It
// is a no-op unless invoked by them.
func TestHelperProcessMtixCLI(_ *testing.T) {
	if os.Getenv("MTIX_TEST_HELPER_PROCESS") != "1" {
		return
	}
	args := strings.Fields(os.Getenv("MTIX_TEST_HELPER_ARGS"))
	err := runArgs(args)
	_ = closeApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// --- mtix import --mode replace ---

func writeExportOf(t *testing.T) string {
	t.Helper()
	exportData, err := app.store.Export(context.Background(), "TEST", "test")
	require.NoError(t, err)
	data, err := json.MarshalIndent(exportData, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "export.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestImportReplace_PipedStdinRefusesAndMutatesNothing(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 2)
	path := writeExportOf(t)
	seedTickets(t, 1) // a ticket the file does not have — replace would lose it
	answerDestructive(t, "3", false)

	err := runImport(path, importFlags{mode: "replace"})
	require.ErrorIs(t, err, errDestructiveRefused)
	assert.Contains(t, err.Error(), "mtix import --mode replace")
	assert.Equal(t, 3, allNodeCount(t))
	assert.Empty(t, preDestroySnapshots(t, "import-replace"))
}

func TestImportReplace_WrongValueAbortsAndMutatesNothing(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 2)
	path := writeExportOf(t)
	seedTickets(t, 1)
	answerDestructive(t, "2", true) // the file's count, not the store's

	err := runImport(path, importFlags{mode: "replace"})
	require.ErrorIs(t, err, errDestructiveRefused)
	assert.Equal(t, 3, allNodeCount(t))
}

func TestImportReplace_ExactCountProceedsAfterSnapshot(t *testing.T) {
	initTestApp(t)
	seedTickets(t, 2)
	path := writeExportOf(t)
	seedTickets(t, 1)
	typeTicketCount(t)

	require.NoError(t, runImport(path, importFlags{mode: "replace"}))
	assert.Equal(t, 2, allNodeCount(t), "the store now holds exactly the file's nodes")
	snaps := preDestroySnapshots(t, "import-replace")
	require.Len(t, snaps, 1)
	assert.Equal(t, 3, nodeCountInSnapshot(t, snaps[0]), "the snapshot keeps the ticket the file lacked")
}

func TestImportReplace_EmptyStoreNeedsNoConfirmation(t *testing.T) {
	// `mtix recover` tells the operator to replace-import into a FRESH
	// project. With nothing to destroy there is no prompt, even on a pipe.
	initTestApp(t)
	seedTickets(t, 2)
	path := writeExportOf(t)
	// Start over with an empty store.
	initTestApp(t)
	answerDestructive(t, "", false)

	require.NoError(t, runImport(path, importFlags{mode: "replace"}))
	assert.Equal(t, 2, allNodeCount(t))
	assert.Empty(t, preDestroySnapshots(t, "import-replace"), "no snapshot of an empty store")
}

// --- the flags say what they do ---

func TestReconcileHelp_StatesDiscardLocalDeletesEveryTicket(t *testing.T) {
	cmd := newSyncReconcileCmd()
	assert.Contains(t, cmd.Long, "DELETE EVERY TICKET in the local store")
	assert.Contains(t, cmd.Long, "No\nflag satisfies it (--yes does not)")
	assert.Contains(t, cmd.Flags().Lookup("discard-local").Usage, "DELETE every ticket")
	assert.Contains(t, cmd.Flags().Lookup("yes").Usage, "does NOT bypass")
	assert.NotContains(t, cmd.Long, "drop local nodes/events")
}
