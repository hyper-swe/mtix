// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Real-hub, real-binary proof of the pushed-work claims of MTIX-112.17.16.18
// (ruling r4 points 1 and 3): a pushed delete and a pushed rerun per strategy
// enrol, with history.export on and off; a refusal's digest holds across
// refusals seconds apart; a held field and the printed dependency advice are
// followed as printed. Gated on MTIX_PG_TEST_DSN through requireCmdPG.

const baseConfig = "prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"

// binaryLine runs one command line through sh in the worktree with mtix bound to
// the built binary and the worktree's config set to baseConfig plus extra.
func (f *enrolPGFixture) binaryLine(t *testing.T, binary, extraConfig, line string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "config.yaml"), []byte(baseConfig+extraConfig), 0o600))
	cmd := exec.CommandContext(t.Context(), "sh", "-c", `mtix() { "$MTIX_BIN" "$@"; }; `+line) //nolint:gosec // G204: the command under test
	cmd.Dir = f.root
	cmd.Env = subprocessEnv(t, binary)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s: %s", line, out)
}

// writeBoardAs writes tasks.json the way the worktree's own config exports it.
func (f *enrolPGFixture) writeBoardAs(t *testing.T, extraConfig string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "config.yaml"), []byte(baseConfig+extraConfig), 0o600))
	require.NoError(t, withBoardStore(slog.Default(), f.root, f.data, func(st *sqlite.Store) error {
		return service.NewSyncService(st, slog.Default(), time.Now).AutoExport(context.Background(), filepath.Join(f.root, ".mtix"))
	}))
}

// pushedTree gives TEST-1 a mixed subtree and pushes it: a done child, a claimed
// child, a cancelled child, an open child with a grandchild, and a dependency.
func (f *enrolPGFixture) pushedTree(t *testing.T) {
	t.Helper()
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		for i, id := range []string{"TEST-1.1", "TEST-1.2", "TEST-1.3", "TEST-1.4"} {
			require.NoError(t, st.CreateNode(ctx, mkPGNode(id, "TEST-1", 1, i+1, "child "+id)))
		}
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1.4.1", "TEST-1.4", 2, 1, "grandchild")))
		require.NoError(t, st.ClaimNode(ctx, "TEST-1.1", "lane-a"))
		require.NoError(t, st.TransitionStatus(ctx, "TEST-1.1", model.StatusInProgress, "start", "lane-a"))
		require.NoError(t, st.TransitionStatus(ctx, "TEST-1.1", model.StatusDone, "finished", "lane-a"))
		require.NoError(t, st.ClaimNode(ctx, "TEST-1.2", "lane-a"))
		require.NoError(t, st.CancelNode(ctx, "TEST-1.3", "not needed", "lane-a", false))
		require.NoError(t, st.AddDependency(ctx, &model.Dependency{FromID: "TEST-1.4", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}))
	})
}

// TestEnrolWorktree_RealHub_PushedRerunAndDelete_Enrols_PG: a rerun of each
// strategy and a plain delete, run through the built binary, pushed, enrol with
// no step, with history.export off and on (the history row of a deletion holds
// the deleting clone's clock).
func TestEnrolWorktree_RealHub_PushedRerunAndDelete_Enrols_PG(t *testing.T) {
	binary := realMtix(t)
	for _, history := range []string{"", historyConfig} {
		for name, line := range map[string]string{
			"rerun all":       "mtix rerun TEST-1 --strategy all --reason again",
			"rerun review":    "mtix rerun TEST-1 --strategy review --reason again",
			"rerun delete":    "mtix rerun TEST-1 --strategy delete --reason again",
			"rerun open_only": "mtix rerun TEST-1 --strategy open_only --reason again",
			"plain delete":    "mtix delete TEST-2",
		} {
			t.Run(name+map[bool]string{true: " with history", false: ""}[history != ""], func(t *testing.T) {
				f := newEnrolPGFixture(t)
				f.pushedTree(t)
				f.binaryLine(t, binary, history, line)
				f.binaryLine(t, binary, history, "mtix sync push --insecure-tls")
				f.writeBoardAs(t, history)

				_, err := f.enrol()

				var refusal *service.WorktreeBoardError
				if errors.As(err, &refusal) {
					t.Logf("refused: %v", refusal.Diff)
				}
				require.NoError(t, err, "a fully pushed %s enrols", name)
				require.Len(t, f.enrolled, 1)
			})
		}
	}
}

// TestEnrolWorktree_RealHub_DeleteRefusalDigest_HoldsAcrossRefusals_PG: a board
// that refuses on a local change and holds a pushed delete (history.export on)
// names the same digest on two refusals seconds apart, and accept with the
// second one enrols.
func TestEnrolWorktree_RealHub_DeleteRefusalDigest_HoldsAcrossRefusals_PG(t *testing.T) {
	binary := realMtix(t)
	f := newEnrolPGFixture(t)
	f.binaryLine(t, binary, historyConfig, "mtix delete TEST-2")
	f.binaryLine(t, binary, historyConfig, "mtix sync push --insecure-tls")
	st := f.open(t)
	_, err := st.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET title = 'imported title' WHERE id = 'TEST-1'`)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	f.writeBoardAs(t, historyConfig)

	first := f.refusalOf(t)
	time.Sleep(2 * time.Second)
	second := f.refusalOf(t)

	assert.Equal(t, service.BoardHubDiffers, first.Reason)
	assert.Equal(t, first.Digest(), second.Digest(), "an unchanged board names one digest")
	assert.NotContains(t, first.Diff["TEST-2"], "history", "the deletion clock is not a difference")
	f.acceptFinding(t, second)
	require.Len(t, f.enrolled, 1)
}

// heldFieldNode is a node both the store (unjournaled) and the hub hold, with
// another description on each side.
func heldFieldNode(description string) *model.Node {
	n := mkPGNode("TEST-30", "", 0, 30, "held by the hub")
	n.Description = description
	n.ContentHash = n.ComputeHash()
	return n
}

// TestEnrolWorktree_RealHub_HeldField_NamedInTextAndReport_ThenStepThreeKeepsTheHubValue_PG:
// the text and the written report name the field in which the store differs from
// the hub, the digest follows the value, and the printed step (3) leaves one
// node holding the hub's value while the old store keeps its own.
func TestEnrolWorktree_RealHub_HeldField_NamedInTextAndReport_ThenStepThreeKeepsTheHubValue_PG(t *testing.T) {
	ctx := context.Background()
	f := newEnrolPGFixture(t)
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		require.NoError(t, peer.CreateNode(ctx, heldFieldNode("hub description")))
	})
	f.storeOnlyUnjournaled(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.CreateNode(ctx, heldFieldNode("local description")))
	}, "TEST-30")

	refusal := f.refusalOf(t)

	assert.Contains(t, refusal.Error(), "the hub already holds TEST-30")
	assert.Regexp(t, regexp.MustCompile(`differs there in TEST-30: [a-z_, ]*description`), refusal.Error())
	var report struct {
		Held map[string][]string `json:"held_fields"`
	}
	body, err := os.ReadFile(refusal.Report)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &report))
	assert.Contains(t, report.Held["TEST-30"], "description")
	assert.NotContains(t, string(body), "local description", "names only, no content")

	res, err := f.service().EnrolWorktree(ctx, service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test", AcceptHub: refusal.Digest()})
	require.NoError(t, err)
	assert.Equal(t, 1, f.countTitled(t, "held by the hub"))
	got, err := f.open(t).GetNode(ctx, "TEST-30")
	require.NoError(t, err)
	assert.Equal(t, "hub description", got.Description, "step (3) gives the hub's value")
	aside, err := sqlite.New(res.AsideDir, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = aside.Close() })
	kept, err := aside.GetNode(ctx, "TEST-30")
	require.NoError(t, err)
	assert.Equal(t, "local description", kept.Description, "the old store keeps its value")
}

// TestEnrolWorktree_RealHub_PrintedDependencyAdvice_FollowedEndToEnd_PG: the
// text puts step (3) before mtix dep add. Run as printed, dep add before the
// accept is refused (the store holds the link), after the accept it works, a push
// sends it and a peer pulls one dependency row.
func TestEnrolWorktree_RealHub_PrintedDependencyAdvice_FollowedEndToEnd_PG(t *testing.T) {
	binary := realMtix(t)
	f := newEnrolPGFixture(t)
	dep := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}
	f.storeOnlyUnjournaled(t, func(ctx context.Context, st *sqlite.Store) { require.NoError(t, st.AddDependency(ctx, dep)) }, "TEST-1")

	refusal := f.refusalOf(t)

	msg := refusal.Error()
	assert.Contains(t, msg, "dependency row(s) from TEST-1")
	assert.Contains(t, msg, "so use step (3), then add the link again with mtix dep add")
	require.Error(t, tryBinaryLine(t, f, binary, "mtix dep add TEST-1 TEST-2"), "before the accept the store holds the link")
	f.acceptFinding(t, refusal)
	f.binaryLine(t, binary, "", "mtix dep add TEST-1 TEST-2")
	f.binaryLine(t, binary, "", "mtix sync push --insecure-tls")

	var rows int
	f.teammate(t, func(ctx context.Context, peer *sqlite.Store) {
		exported, err := peer.Export(ctx, "", "")
		require.NoError(t, err)
		rows = len(exported.Dependencies)
	})
	assert.Equal(t, 1, rows, "a peer pulls the dependency row")
}

// tryBinaryLine is binaryLine returning the error instead of failing.
func tryBinaryLine(t *testing.T, f *enrolPGFixture, binary, line string) error {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "config.yaml"), []byte(baseConfig), 0o600))
	cmd := exec.CommandContext(t.Context(), "sh", "-c", `mtix() { "$MTIX_BIN" "$@"; }; `+line) //nolint:gosec // G204: the command under test
	cmd.Dir = f.root
	cmd.Env = subprocessEnv(t, binary)
	out, err := cmd.CombinedOutput()
	t.Logf("%s: %s", line, out)
	return err
}
