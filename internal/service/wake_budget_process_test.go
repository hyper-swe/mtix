// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestWakeBudget_CrossProcessBoards_OnlyOneLastSlot(t *testing.T) {
	f := newWakeBudgetFixture(t, "1")
	a, b := f.board(t, 1), f.board(t, 1)
	dir := t.TempDir()
	start, result := filepath.Join(dir, "go"), filepath.Join(dir, "sends")
	require.NoError(t, os.WriteFile(result, nil, 0o600))
	type child struct {
		cmd   *exec.Cmd
		log   *bytes.Buffer
		ready string
	}
	children := make([]child, 0, 2)
	for i, board := range []*wakeBudgetBoard{a, b} {
		ready := filepath.Join(dir, "ready-"+strconv.Itoa(i))
		cmd, log := startWakeBudgetChild(t, board, ready, start, result)
		children = append(children, child{cmd: cmd, log: log, ready: ready})
	}
	for _, child := range children {
		waitWakeBudgetFile(t, child.ready)
	}
	require.NoError(t, os.WriteFile(start, []byte("go"), 0o600))
	for _, child := range children {
		require.NoError(t, child.cmd.Wait(), "%s", child.log.String())
	}
	raw, err := os.ReadFile(result)
	require.NoError(t, err)
	require.Equal(t, "wake\n", string(raw), "independent processes share the recipient's last slot")
	require.Equal(t, int64(1), budgetLedgerRows(t, a)+budgetLedgerRows(t, b))
}

func startWakeBudgetChild(t *testing.T, board *wakeBudgetBoard, ready, start, result string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWakeBudgetProcessHelper$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "MTIX_TEST_BUDGET_CHILD=1", "MTIX_TEST_BUDGET_DB="+board.path,
		"MTIX_TEST_BUDGET_SEQ="+strconv.FormatInt(board.keys[0].EventSeq, 10), "MTIX_TEST_BUDGET_UID="+board.keys[0].UID,
		"MTIX_TEST_BUDGET_READY="+ready, "MTIX_TEST_BUDGET_START="+start, "MTIX_TEST_BUDGET_RESULT="+result)
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Logf("stop budget child: %v", err)
			}
			if err := cmd.Wait(); err != nil {
				t.Logf("reap budget child: %v", err)
			}
		}
	})
	return cmd, &log
}

func waitWakeBudgetFile(t *testing.T, path string) {
	t.Helper()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		_, err := os.Stat(path)
		if err == nil {
			return
		}
		require.ErrorIs(t, err, os.ErrNotExist)
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-timeout.C:
			t.Fatal("budget process barrier timed out")
		case <-tick.C:
		}
	}
}

// TestWakeBudgetProcessHelper is the real dispatcher subprocess entry point.
func TestWakeBudgetProcessHelper(t *testing.T) {
	if os.Getenv("MTIX_TEST_BUDGET_CHILD") != "1" {
		return
	}
	for _, name := range []string{"DB", "SEQ", "UID", "READY", "START", "RESULT"} {
		require.NotEmpty(t, os.Getenv("MTIX_TEST_BUDGET_"+name))
	}
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	host, err := state.ReadHostID()
	require.NoError(t, err)
	store, err := sqlite.New(os.Getenv("MTIX_TEST_BUDGET_DB"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	clock := func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	store.SetClock(clock)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: store, BudgetState: state, Settings: state, Clock: clock,
		Alerts: service.NewAlerts(state, clock), Held: func(string) {}, Send: recordWakeBudgetProcessSend,
	})
	require.NoError(t, err)
	seq, err := strconv.ParseInt(os.Getenv("MTIX_TEST_BUDGET_SEQ"), 10, 64)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("MTIX_TEST_BUDGET_READY"), nil, 0o600))
	waitWakeBudgetFile(t, os.Getenv("MTIX_TEST_BUDGET_START"))
	key := model.WakeKey{AgentID: "budget-worker", HostID: host, UID: os.Getenv("MTIX_TEST_BUDGET_UID"), EventSeq: seq, Epoch: 1}
	require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
}

func recordWakeBudgetProcessSend(context.Context, model.WakeAttempt) (err error) {
	f, err := os.OpenFile(os.Getenv("MTIX_TEST_BUDGET_RESULT"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	_, err = f.WriteString("wake\n")
	return err
}
