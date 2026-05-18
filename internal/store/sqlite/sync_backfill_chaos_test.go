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
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestBackfill_AtomicityUnderKill is the MTIX-15.13.1 N1/N2 chaos
// test. Mirrors TestApply_AtomicityUnderKill: spawn a child process,
// SIGKILL it at parameterized delays during backfill, then assert
// the post-mortem invariant.
//
// Invariant: after the child dies, either the backfill committed
// fully (sync_events count > 0 AND equal to the expected count from
// a clean dry-run) OR rolled back fully (sync_events count == 0).
// Never a partial state — the load-bearing safety property.
//
// Skipped on Windows; SIGKILL semantics differ.
func TestBackfill_AtomicityUnderKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on Windows")
	}
	if os.Getenv("MTIX_BACKFILL_CHAOS_CHILD") != "" {
		runBackfillChaosChild(t)
		return
	}

	const iterations = 20
	for i := 0; i < iterations; i++ {
		i := i
		t.Run("iter-"+strconv.Itoa(i), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "backfill-chaos.db")

			// Pre-seed the DB with N nodes via a non-chaos path.
			s, err := sqlite.New(dbPath, slog.Default())
			require.NoError(t, err)

			const seedNodes = 5
			for j := 0; j < seedNodes; j++ {
				node := &model.Node{
					ID:        "TEST-" + strconv.Itoa(j+1),
					Project:   "TEST",
					Title:     "seed-" + strconv.Itoa(j+1),
					Status:    model.StatusOpen,
					NodeType:  model.NodeTypeAuto,
					Priority:  model.PriorityMedium,
					Weight:    1.0,
					Creator:   "test",
					CreatedAt: time.Now().UTC(),
					UpdatedAt: time.Now().UTC(),
					Depth:     0,
					Seq:       j + 1,
				}
				require.NoError(t, s.CreateNode(context.Background(), node))
			}

			// Wipe sync_events to simulate the upgrader state.
			_, err = s.WriteDB().ExecContext(context.Background(),
				`DELETE FROM sync_events`)
			require.NoError(t, err)

			// Confirm the wipe.
			var pre int
			require.NoError(t, s.WriteDB().QueryRowContext(context.Background(),
				`SELECT count(*) FROM sync_events`).Scan(&pre))
			require.Equal(t, 0, pre)

			require.NoError(t, s.Close())

			killDelay := time.Duration((i*5)%50) * time.Millisecond

			cmd := exec.Command(os.Args[0],
				"-test.run=TestBackfill_AtomicityUnderKill",
				"-test.v=false",
				"-test.timeout=30s",
			)
			cmd.Env = append(os.Environ(),
				"MTIX_BACKFILL_CHAOS_CHILD=1",
				"MTIX_BACKFILL_CHAOS_DB="+dbPath,
			)
			cmd.Stdout = os.Stderr
			cmd.Stderr = os.Stderr
			require.NoError(t, cmd.Start())

			time.Sleep(killDelay)
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()

			// Re-open and verify atomicity.
			raw, err := sql.Open("sqlite", dbPath)
			require.NoError(t, err)
			defer func() { _ = raw.Close() }()

			var events int
			require.NoError(t, raw.QueryRow(
				`SELECT count(*) FROM sync_events`,
			).Scan(&events))

			// Atomicity invariant: either 0 (rolled back) OR exactly the
			// committed count (which is at least seedNodes for create_node
			// events; the per-node update_field count is variable but
			// non-zero only if descriptions/prompts were set, which our
			// seed doesn't do). For this seed shape: 0 or 5.
			require.Truef(t, events == 0 || events == seedNodes,
				"atomicity violated: sync_events=%d (expected 0 or %d) delay=%v",
				events, seedNodes, killDelay)

			// Nodes table is unchanged by backfill in any case.
			var nodes int
			require.NoError(t, raw.QueryRow(
				`SELECT count(*) FROM nodes WHERE deleted_at IS NULL`,
			).Scan(&nodes))
			require.Equal(t, seedNodes, nodes,
				"nodes table corrupted: backfill must be non-destructive on nodes")
		})
	}
}

// runBackfillChaosChild executes one Backfill call inside a child
// process. The parent SIGKILLs us mid-flight; whichever side of the
// SQLite COMMIT we're on determines the post-recovery state.
func runBackfillChaosChild(t *testing.T) {
	t.Helper()
	dbPath := os.Getenv("MTIX_BACKFILL_CHAOS_DB")
	if dbPath == "" {
		return
	}
	s, err := sqlite.New(dbPath, slog.Default())
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = s.Close() }()

	_, err = s.Backfill(context.Background(), false)
	if err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
