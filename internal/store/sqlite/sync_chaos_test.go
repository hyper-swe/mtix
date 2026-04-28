// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestEmit_AtomicityUnderKill is the FR-18.3 / MTIX-15.2.3 chaos test.
//
// Strategy: spawn a child process that opens the same SQLite DB and
// performs a CreateNode mutation. The parent SIGKILLs the child after a
// random delay between 0 and 100ms — sometimes before the COMMIT,
// sometimes after. Then the parent re-opens the DB and asserts the
// invariant: nodes row and sync_events row either both exist (commit
// landed before kill) or neither exists (rollback). NEVER one without
// the other.
//
// Skipped on Windows because syscall.SIGKILL semantics differ; tracked
// for cross-platform parity in MTIX-15.2.4 + 15.9.
func TestEmit_AtomicityUnderKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on Windows; covered by separate platform test in MTIX-15.9")
	}
	if os.Getenv("MTIX_CHAOS_CHILD") != "" {
		// We are the child process. Run the mutation and exit naturally
		// if not killed in time.
		runChaosChild(t)
		return
	}

	// Re-exec ourselves with the chaos child env var set, against a
	// per-iteration DB path. Parameterize the kill delay so we hit
	// pre-INSERT, mid-tx, post-emit, and post-COMMIT.
	const iterations = 30
	for i := 0; i < iterations; i++ {
		i := i
		t.Run("iter-"+strconv.Itoa(i), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "chaos.db")

			// Pre-init the schema so the child only does the mutation
			// (the schema migration is not what we're stress-testing).
			s, err := sqlite.New(dbPath, slog.Default())
			require.NoError(t, err)
			require.NoError(t, s.Close())

			killDelay := time.Duration((i*7)%100) * time.Millisecond

			cmd := exec.Command(os.Args[0],
				"-test.run=TestEmit_AtomicityUnderKill",
				"-test.v=false",
				"-test.timeout=30s",
			)
			cmd.Env = append(os.Environ(),
				"MTIX_CHAOS_CHILD=1",
				"MTIX_CHAOS_DB="+dbPath,
				"MTIX_CHAOS_NODE_ID=MTIX-"+strconv.Itoa(i+1),
			)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			require.NoError(t, cmd.Start())

			// Wait the parameterized delay then SIGKILL.
			time.Sleep(killDelay)
			_ = cmd.Process.Signal(syscall.SIGKILL)
			_, _ = cmd.Process.Wait()

			// Re-open and verify atomicity invariant.
			raw, err := sql.Open("sqlite", dbPath)
			require.NoError(t, err)
			defer func() { _ = raw.Close() }()

			nodeID := "MTIX-" + strconv.Itoa(i+1)
			var nodes int
			require.NoError(t, raw.QueryRow(
				`SELECT COUNT(*) FROM nodes WHERE id = ?`, nodeID,
			).Scan(&nodes))

			var events int
			require.NoError(t, raw.QueryRow(
				`SELECT COUNT(*) FROM sync_events WHERE node_id = ?`, nodeID,
			).Scan(&events))

			require.Equalf(t, nodes, events,
				"atomicity invariant violated: nodes=%d events=%d (delay=%v)",
				nodes, events, killDelay)
		})
	}
}

func runChaosChild(t *testing.T) {
	t.Helper()
	dbPath := os.Getenv("MTIX_CHAOS_DB")
	nodeID := os.Getenv("MTIX_CHAOS_NODE_ID")
	if dbPath == "" || nodeID == "" {
		// Not actually a child invocation; just skip silently.
		return
	}

	s, err := sqlite.New(dbPath, slog.Default())
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = s.Close() }()

	node := &model.Node{
		ID:        nodeID,
		Project:   "MTIX",
		Title:     "chaos " + nodeID,
		Status:    model.StatusOpen,
		NodeType:  model.NodeTypeEpic,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	// CreateNode wraps in a tx; the COMMIT happens inside WithTx.
	// SIGKILL between the writes inside the tx leaves SQLite WAL in a
	// state from which the next opener recovers atomically (either
	// the whole tx or none).
	if err := s.CreateNode(context.Background(), node); err != nil {
		os.Exit(3)
	}
	// Successful exit only if the kill missed.
	os.Exit(0)
}

// TestEmit_AtomicityUnderInProcessRollback is a faster companion to the
// SIGKILL test that exercises the same property without spawning subprocs.
// Useful in the regression CI loop where 30 subprocesses would dominate
// runtime; the kill-9 test can run with a smaller iteration count there.
func TestEmit_AtomicityUnderInProcessRollback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rb.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	const tries = 20
	var wg sync.WaitGroup
	wg.Add(tries)
	for i := 0; i < tries; i++ {
		go func(i int) {
			defer wg.Done()
			// Half: parent missing -> CreateNode fails -> rollback.
			// Half: parent provided as the prior iteration's id -> success.
			node := &model.Node{
				ID:        "MTIX-" + strconv.Itoa(i+1),
				Project:   "MTIX",
				Title:     "n",
				Status:    model.StatusOpen,
				NodeType:  model.NodeTypeIssue,
				Priority:  model.PriorityMedium,
				Weight:    1.0,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}
			if i%2 == 0 {
				node.ParentID = "GHOST-PARENT" // forces rollback
			} else {
				node.NodeType = model.NodeTypeEpic // root node, no parent
			}
			_ = s.CreateNode(context.Background(), node)
		}(i)
	}
	wg.Wait()

	// Invariant: every nodes row has exactly one matching sync_events
	// row, and vice versa. No orphans either way.
	var pairs int
	require.NoError(t, raw.QueryRow(`
		SELECT COUNT(*) FROM nodes n
		JOIN sync_events e ON e.node_id = n.id
		WHERE e.op_type = 'create_node'`).Scan(&pairs))

	var nodes int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&nodes))

	var events int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM sync_events WHERE op_type = 'create_node'`,
	).Scan(&events))

	require.Equal(t, nodes, events,
		"every persisted node MUST have exactly one create_node event (no orphans either way)")
	require.Equal(t, pairs, events,
		"every event MUST join to its node")
}
