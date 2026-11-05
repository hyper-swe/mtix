// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

var enrolNow = time.Date(2026, 11, 3, 10, 20, 30, 0, time.UTC)

// enrolWorld is a git worktree on disk with a store directory and a recorder
// for every port call.
type enrolWorld struct {
	t       *testing.T
	root    string
	data    string
	calls   []string
	events  []sqlite.LocalEvent
	missing []string
	carry   *sqlite.InboxCarry
	fail    map[string]error
	hubConn bool
	svc     *service.WorktreeEnrolment
	opts    service.WorktreeEnrolOptions
}

func newEnrolWorld(t *testing.T) *enrolWorld {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	common := filepath.Join(root, "common", ".git")
	admin := filepath.Join(common, "worktrees", "lane")
	wt := filepath.Join(root, "wt")
	require.NoError(t, os.MkdirAll(admin, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(wt, ".mtix", "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o600))
	w := &enrolWorld{t: t, root: wt, data: filepath.Join(wt, ".mtix", "data"), fail: map[string]error{}, hubConn: true,
		carry: &sqlite.InboxCarry{Acks: []sqlite.CarriedAck{{AgentID: "a", EventID: "e1"}}}}
	require.NoError(t, os.WriteFile(filepath.Join(w.data, "mtix.db"), []byte("OLD-STORE-BYTES"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(w.data, "mtix.db-wal"), []byte("OLD-WAL"), 0o600))
	w.opts = service.WorktreeEnrolOptions{Clock: func() time.Time { return enrolNow }, Ports: w.ports()}
	w.svc = service.NewWorktreeEnrolment(w.opts)
	return w
}

func (w *enrolWorld) step(name string) error {
	w.calls = append(w.calls, name)
	return w.fail[name]
}

func (w *enrolWorld) ports() service.WorktreePorts {
	return service.WorktreePorts{
		Snapshot: func(context.Context, string) (*service.WorktreeSnapshot, error) {
			if err := w.step("snapshot"); err != nil {
				return nil, err
			}
			return &service.WorktreeSnapshot{HubConnected: w.hubConn, Events: w.events, Carry: w.carry}, nil
		},
		HubMissing: func(_ context.Context, _ string, ids []string) ([]string, error) {
			if err := w.step("hub"); err != nil {
				return nil, err
			}
			var out []string
			for _, id := range ids {
				for _, m := range w.missing {
					if id == m {
						out = append(out, id)
					}
				}
			}
			return out, nil
		},
		Backup: func(_ context.Context, _, dest string) error {
			if err := w.step("backup"); err != nil {
				return err
			}
			return os.WriteFile(dest, []byte("BACKUP"), 0o600)
		},
		Clone: func(_ context.Context, root string) error {
			err := w.step("clone")
			require.NoError(w.t, os.MkdirAll(filepath.Join(root, ".mtix", "data"), 0o700))
			require.NoError(w.t, os.WriteFile(filepath.Join(root, ".mtix", "data", "mtix.db"), []byte("CLONED"), 0o600))
			return err
		},
		ImportCarry: func(_ context.Context, _ string, c *sqlite.InboxCarry, _ time.Time) error {
			w.t.Helper()
			assert.Same(w.t, w.carry, c, "the snapshot's carry is imported unchanged")
			return w.step("carry")
		},
		Enroll: func(context.Context, service.EnrollmentRequest) error { return w.step("enrol") },
	}
}

func (w *enrolWorld) run() (*service.WorktreeEnrolResult, error) {
	return w.svc.EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: w.root, Mode: "yes", Source: "test"})
}

// treeHash hashes names, modes and bytes of everything under dir.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	var paths []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		paths = append(paths, p)
		return err
	}))
	sort.Strings(paths)
	for _, p := range paths {
		info, err := os.Lstat(p)
		require.NoError(t, err)
		fmt.Fprintf(h, "%s|%v|", strings.TrimPrefix(p, dir), info.Mode())
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			require.NoError(t, err)
			h.Write(b)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestEnrolWorktree_PendingLocalEvents_RefusedWithList(t *testing.T) {
	at := time.Date(2026, 11, 2, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name    string
		events  []sqlite.LocalEvent
		missing []string
		want    []string
	}{
		{"pending", []sqlite.LocalEvent{{ID: "ev-p", NodeID: "PROJ-1", Op: "comment", Status: "pending", At: at}}, nil, []string{"ev-p PROJ-1 comment 2026-11-02T01:02:03Z"}},
		{"conflicted", []sqlite.LocalEvent{{ID: "ev-c", NodeID: "PROJ-2", Op: "claim", Status: "conflicted", At: at}}, nil, []string{"ev-c PROJ-2 claim"}},
		{"hub lacks a pushed event", []sqlite.LocalEvent{{ID: "ev-x", NodeID: "PROJ-3", Op: "update_field", Status: "pushed", At: at}}, []string{"ev-x"}, []string{"ev-x PROJ-3 update_field", "not on the hub"}},
		{"hub lacks an applied event", []sqlite.LocalEvent{{ID: "ev-y", NodeID: "PROJ-4", Op: "create_node", Status: "applied", At: at}}, []string{"ev-y"}, []string{"ev-y PROJ-4 create_node"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newEnrolWorld(t)
			w.events = append([]sqlite.LocalEvent{{ID: "ev-ok", NodeID: "PROJ-0", Op: "create_node", Status: "applied", At: at}}, tt.events...)
			w.missing = tt.missing
			before := treeHash(t, w.data)

			_, err := w.run()

			var refusal *service.WorktreeLocalEventsError
			require.ErrorAs(t, err, &refusal)
			require.ErrorIs(t, err, model.ErrConflict)
			assert.Len(t, refusal.Events, 1)
			for _, s := range tt.want {
				assert.Contains(t, err.Error(), s)
			}
			assert.Contains(t, err.Error(), "recovery:")
			assert.NotContains(t, err.Error(), "ev-ok")
			assert.Equal(t, before, treeHash(t, w.data), "a refusal touches nothing")
			_, bkErr := os.Lstat(filepath.Join(w.root, ".mtix", "backups"))
			assert.ErrorIs(t, bkErr, os.ErrNotExist, "and writes no backup")
			assert.NotContains(t, w.calls, "backup")
			assert.NotContains(t, w.calls, "clone")
			assert.NotContains(t, w.calls, "enrol")
		})
	}
}

func TestEnrolWorktree_AllLocalOnlyEventsListed_NoCap(t *testing.T) {
	w := newEnrolWorld(t)
	for i := 0; i < 120; i++ {
		w.events = append(w.events, sqlite.LocalEvent{ID: fmt.Sprintf("ev-%03d", i), NodeID: "PROJ-1", Op: "comment", Status: "pending", At: enrolNow})
	}
	_, err := w.run()
	var refusal *service.WorktreeLocalEventsError
	require.ErrorAs(t, err, &refusal)
	assert.Len(t, refusal.Events, 120)
	assert.Contains(t, err.Error(), "ev-119")
}

func TestEnrolWorktree_UsedStore_BackedUpMovedAndCloned(t *testing.T) {
	w := newEnrolWorld(t)
	w.events = []sqlite.LocalEvent{{ID: "ev-1", NodeID: "PROJ-1", Op: "create_node", Status: "applied", At: enrolNow}}

	res, err := w.run()
	require.NoError(t, err)

	assert.Equal(t, []string{"snapshot", "hub", "backup", "clone", "carry", "enrol"}, w.calls)
	assert.Equal(t, filepath.Join(w.root, ".mtix", "data.pre-enrol-20261103T102030Z"), res.AsideDir)
	old, err := os.ReadFile(filepath.Join(res.AsideDir, "mtix.db"))
	require.NoError(t, err)
	assert.Equal(t, "OLD-STORE-BYTES", string(old), "the old store is moved aside whole")
	wal, err := os.ReadFile(filepath.Join(res.AsideDir, "mtix.db-wal"))
	require.NoError(t, err)
	assert.Equal(t, "OLD-WAL", string(wal))
	cloned, err := os.ReadFile(filepath.Join(w.data, "mtix.db"))
	require.NoError(t, err)
	assert.Equal(t, "CLONED", string(cloned), "the store in use was seeded by the clone")
	bk, err := os.ReadFile(res.BackupPath)
	require.NoError(t, err)
	assert.Equal(t, "BACKUP", string(bk))
	assert.True(t, strings.HasPrefix(res.BackupPath, filepath.Join(w.root, ".mtix")))
}

func TestEnrolWorktree_FailureAfterMove_RestoresByteIdentical(t *testing.T) {
	boom := errors.New("injected failure")
	for _, step := range []string{"clone", "carry", "enrol"} {
		t.Run(step, func(t *testing.T) {
			w := newEnrolWorld(t)
			w.fail[step] = boom
			before := treeHash(t, w.data)

			_, err := w.run()

			require.ErrorIs(t, err, boom)
			assert.Contains(t, err.Error(), "original store was restored")
			assert.Equal(t, before, treeHash(t, w.data), "the store is exactly as it was")
			_, statErr := os.Lstat(filepath.Join(w.root, ".mtix", "data.pre-enrol-20261103T102030Z"))
			assert.ErrorIs(t, statErr, os.ErrNotExist, "the aside directory is gone after a restore")
			if step != "enrol" {
				assert.NotContains(t, w.calls, "enrol")
			}
		})
	}
}

func TestEnrolWorktree_FailureBeforeMove_NothingChanges(t *testing.T) {
	boom := errors.New("injected failure")
	for _, step := range []string{"snapshot", "hub", "backup"} {
		t.Run(step, func(t *testing.T) {
			w := newEnrolWorld(t)
			w.events = []sqlite.LocalEvent{{ID: "ev-1", NodeID: "PROJ-1", Op: "create_node", Status: "applied", At: enrolNow}}
			w.fail[step] = boom
			before := treeHash(t, w.data)
			_, err := w.run()
			require.ErrorIs(t, err, boom)
			assert.Equal(t, before, treeHash(t, w.data))
			assert.NotContains(t, w.calls, "clone")
			assert.NotContains(t, w.calls, "enrol")
		})
	}
}

func TestEnrolWorktree_MoveAsideFails_NothingChanges(t *testing.T) {
	w := newEnrolWorld(t)
	boom := errors.New("rename refused")
	w.opts.Rename = func(string, string) error { return boom }
	w.svc = service.NewWorktreeEnrolment(w.opts)
	before := treeHash(t, w.data)
	_, err := w.run()
	require.ErrorIs(t, err, boom)
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "clone")
}

func TestEnrolWorktree_RestoreFails_NamesTheIntactAsideDir(t *testing.T) {
	w := newEnrolWorld(t)
	w.fail["clone"] = errors.New("clone broke")
	renames := 0
	w.opts.Rename = func(a, b string) error {
		renames++
		if renames == 2 {
			return errors.New("restore refused")
		}
		return os.Rename(a, b)
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)
	_, err := w.run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restore refused")
	assert.Contains(t, err.Error(), "clone broke")
	assert.Contains(t, err.Error(), "data.pre-enrol-20261103T102030Z")
	old, readErr := os.ReadFile(filepath.Join(w.root, ".mtix", "data.pre-enrol-20261103T102030Z", "mtix.db"))
	require.NoError(t, readErr)
	assert.Equal(t, "OLD-STORE-BYTES", string(old), "the original is intact where the error says")
}

func TestEnrolWorktree_PartialStoreCannotBeRemoved_NamesTheIntactAsideDir(t *testing.T) {
	w := newEnrolWorld(t)
	w.fail["carry"] = errors.New("carry broke")
	w.opts.RemoveTree = func(string) error { return errors.New("remove refused") }
	w.svc = service.NewWorktreeEnrolment(w.opts)
	_, err := w.run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove refused")
	assert.Contains(t, err.Error(), "carry broke")
	assert.Contains(t, err.Error(), "original store is intact")
}

func TestEnrolWorktree_PriorAsideDir_RefusesWithoutMoving(t *testing.T) {
	w := newEnrolWorld(t)
	aside := filepath.Join(w.root, ".mtix", "data.pre-enrol-20261103T102030Z")
	require.NoError(t, os.MkdirAll(aside, 0o700))
	before := treeHash(t, w.data)
	_, err := w.run()
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "clone")
}

func TestEnrolWorktree_Preconditions_Refused(t *testing.T) {
	t.Run("not a worktree", func(t *testing.T) {
		w := newEnrolWorld(t)
		require.NoError(t, os.Remove(filepath.Join(w.root, ".git")))
		_, err := w.run()
		require.ErrorIs(t, err, model.ErrInvalidInput)
		assert.Empty(t, w.calls)
	})
	t.Run("store not hub connected", func(t *testing.T) {
		w := newEnrolWorld(t)
		w.hubConn = false
		_, err := w.run()
		require.ErrorIs(t, err, model.ErrInvalidInput)
		assert.Equal(t, []string{"snapshot"}, w.calls)
	})
	t.Run("missing ports", func(t *testing.T) {
		svc := service.NewWorktreeEnrolment(service.WorktreeEnrolOptions{})
		_, err := svc.EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: t.TempDir()})
		require.ErrorIs(t, err, model.ErrInvalidInput)
	})
	t.Run("unknown path", func(t *testing.T) {
		w := newEnrolWorld(t)
		_, err := w.svc.EnrolWorktree(context.Background(), service.EnrollmentRequest{Path: filepath.Join(w.root, "nope")})
		require.Error(t, err)
	})
	t.Run("hub unreachable never passes the check", func(t *testing.T) {
		w := newEnrolWorld(t)
		w.events = []sqlite.LocalEvent{{ID: "ev-1", NodeID: "PROJ-1", Op: "create_node", Status: "applied", At: enrolNow}}
		w.fail["hub"] = errors.New("hub down")
		_, err := w.run()
		require.ErrorContains(t, err, "hub down")
		assert.NotContains(t, w.calls, "backup")
	})
}

// TestEnrolWorktree_NeverDiscardLocal: the procedure has no port that discards
// local state, so it cannot call one (ADR-007 D14, section 4.2).
func TestEnrolWorktree_NeverDiscardLocal(t *testing.T) {
	typ := reflect.TypeOf(service.WorktreePorts{})
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		names = append(names, typ.Field(i).Name)
		assert.NotContains(t, strings.ToLower(typ.Field(i).Name), "discard")
		assert.NotContains(t, strings.ToLower(typ.Field(i).Name), "reconcile")
	}
	assert.Equal(t, []string{"Snapshot", "HubMissing", "Backup", "Clone", "ImportCarry", "Enroll"}, names,
		"a new port must be reviewed against the no-discard rule")
	files, err := filepath.Glob("worktree_enrol*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, readErr := os.ReadFile(f)
		require.NoError(t, readErr)
		assert.NotContains(t, strings.ToLower(string(src)), "discard-local", f)
		assert.NotContains(t, string(src), "DiscardLocal", f)
	}
}

func TestEnrolWorktree_ConcurrentEnrolment_Refused(t *testing.T) {
	w := newEnrolWorld(t)
	lock, err := pushlock.AcquireNamed(filepath.Join(w.root, ".mtix", "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Release()) }()
	before := treeHash(t, w.data)

	_, err = w.run()

	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	assert.Empty(t, w.calls, "a second enrolment does nothing")
	assert.Equal(t, before, treeHash(t, w.data))
}
