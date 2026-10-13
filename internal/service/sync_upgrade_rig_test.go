// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

// The upgrade rig: two clones of one git remote. The old side runs a real
// released binary (the board it writes is the genuine 1.0.0 form); the
// upgraded side runs this build in process, as the next command does.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// upgradeRig holds the remote, the released binary and the clones.
type upgradeRig struct {
	t      *testing.T
	bin    string
	home   string
	origin string
}

// newUpgradeRig creates an empty remote and a home for the released binary.
func newUpgradeRig(t *testing.T, bin string) *upgradeRig {
	t.Helper()
	r := &upgradeRig{t: t, bin: bin, home: t.TempDir(), origin: filepath.Join(t.TempDir(), "origin.git")}
	runIn(t, r.home, r.env(), "git", "init", "-q", "--bare", r.origin)
	return r
}

func (r *upgradeRig) env() []string {
	return append(cleanGitEnv(), "HOME="+r.home, "GIT_CONFIG_GLOBAL="+filepath.Join(r.home, "gitconfig"),
		"GIT_AUTHOR_NAME=dev", "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_COMMITTER_NAME=dev",
		"GIT_COMMITTER_EMAIL=dev@example.com")
}

// git runs git in dir.
func (r *upgradeRig) git(dir string, args ...string) string {
	r.t.Helper()
	return runIn(r.t, dir, r.env(), "git", args...)
}

// old runs the released binary in dir.
func (r *upgradeRig) old(dir string, args ...string) string {
	r.t.Helper()
	return runIn(r.t, dir, r.env(), r.bin, args...)
}

// clone clones the remote into a new directory (the first clone also seeds the remote).
func (r *upgradeRig) clone() string {
	r.t.Helper()
	dir := filepath.Join(r.t.TempDir(), "clone")
	r.git(r.home, "clone", "-q", r.origin, dir)
	return dir
}

// publish commits the board in dir and pushes it.
func (r *upgradeRig) publish(dir, message string) {
	r.t.Helper()
	r.git(dir, "add", ".mtix/tasks.json", ".mtix/config.yaml")
	r.git(dir, "commit", "-q", "-m", message)
	r.git(dir, "push", "-q", "-u", "origin", "HEAD")
}

// pull brings the remote's commits into dir the way git pull does.
func (r *upgradeRig) pull(dir string) {
	r.t.Helper()
	r.git(dir, "pull", "-q", "--ff-only")
}

// discardBoard puts the committed board back in dir's working tree, as a
// user does to pull over an unpushed export.
func (r *upgradeRig) discardBoard(dir string) {
	r.t.Helper()
	r.git(dir, "checkout", "--", ".mtix/tasks.json")
}

// currentSide is a clone opened by this build.
type currentSide struct {
	store   *sqlite.Store
	svc     *service.SyncService
	mtixDir string
	notices *bytes.Buffer
}

// upgrade opens the clone's store with this build, as the first command after upgrading does.
func upgrade(t *testing.T, dir string) *currentSide {
	t.Helper()
	mtixDir := filepath.Join(dir, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(mtixDir, "data"), 0o755))
	st, err := sqlite.New(filepath.Join(mtixDir, "data"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	c := &currentSide{store: st, mtixDir: mtixDir, notices: &bytes.Buffer{}}
	c.svc = service.NewSyncService(st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	c.svc.SetNoticeWriter(c.notices)
	return c
}

// export returns the clone's store export.
func (c *currentSide) export(t *testing.T) *sqlite.ExportData {
	t.Helper()
	data, err := c.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	return data
}

// node returns the export's node id.
func nodeOf(t *testing.T, data *sqlite.ExportData, id string) (status, assignee string, annotations, activity int) {
	t.Helper()
	for i := range data.Nodes {
		if n := &data.Nodes[i]; n.ID == id {
			return n.Status, n.Assignee, len(n.Annotations), len(n.Activity)
		}
	}
	require.Failf(t, "node missing", "%s", id)
	return "", "", 0, 0
}

// startOldProject initialises a project with the released binary: tasks PROJ-1 and PROJ-2,
// PROJ-1 claimed by alice and commented on, in the clone dir.
func (r *upgradeRig) startOldProject(dir string) {
	r.t.Helper()
	r.old(dir, "init", "--prefix", "PROJ")
	r.old(dir, "create", "Task one", "--description", "first")
	r.old(dir, "create", "Task two")
	r.old(dir, "claim", "PROJ-1", "--agent", "alice")
	r.old(dir, "comment", "PROJ-1", "note from alice before the upgrade")
	r.publish(dir, "board from the released mtix")
}

// teammateUpgrades makes dir, a clone that pulled the board with the released
// binary, upgrade and claim task as bob, then push the 2.0.0 board.
func (r *upgradeRig) teammateUpgrades(dir, task string) {
	r.t.Helper()
	b := upgrade(r.t, dir)
	ctx := context.Background()
	require.NoError(r.t, b.svc.AutoImport(ctx, b.mtixDir))
	require.NoError(r.t, b.store.ClaimNode(ctx, task, "bob"))
	require.NoError(r.t, b.svc.AutoExport(ctx, b.mtixDir))
	r.publish(dir, "board from the upgraded teammate")
	require.NoError(r.t, b.store.Close())
}

// setupSequence builds the ticket's sequence up to the pull: A and B on the
// released version, A's claim pushed, B pulled, upgraded, claimed PROJ-2 and
// pushed. It returns A's clone, still on the released version and not yet pulled.
func (r *upgradeRig) setupSequence() (a string) {
	r.t.Helper()
	a = r.clone()
	r.startOldProject(a)
	b := r.clone()
	r.old(b, "list")
	r.teammateUpgrades(b, "PROJ-2")
	return a
}

// contains reports whether the notices mention every phrase.
func mentions(notices *bytes.Buffer, phrases ...string) bool {
	text := notices.String()
	for _, p := range phrases {
		if !strings.Contains(text, p) {
			return false
		}
	}
	return true
}

// copyProject copies the project's .mtix directory (store and board) to a
// directory outside any git checkout.
func copyProject(t *testing.T, from string) string {
	t.Helper()
	to := filepath.Join(t.TempDir(), "solo")
	require.NoError(t, filepath.WalkDir(filepath.Join(from, ".mtix"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), raw, 0o644)
	}))
	return to
}
