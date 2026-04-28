// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestApply_AtomicityUnderKill is the FR-18.9 / MTIX-15.4 chaos test.
//
// Strategy: spawn a child process that opens the DB, calls IdempotentApply
// on a known event, and exits. The parent SIGKILLs at parameterized
// delays (0-100ms) — sometimes before the apply commits, sometimes after.
// Then the parent re-opens the DB and asserts the invariant: either both
// the nodes mutation AND the applied_events row exist (commit landed),
// OR neither (rollback). NEVER one without the other.
//
// Skipped on Windows because syscall.SIGKILL semantics differ; cross-
// platform parity tracked for MTIX-15.9.
func TestApply_AtomicityUnderKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on Windows; covered by separate test in MTIX-15.9")
	}
	if os.Getenv("MTIX_APPLY_CHAOS_CHILD") != "" {
		runApplyChaosChild(t)
		return
	}

	const iterations = 25
	for i := 0; i < iterations; i++ {
		i := i
		t.Run("iter-"+strconv.Itoa(i), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "apply-chaos.db")

			// Pre-init the schema so the child only does the apply.
			s, err := sqlite.New(dbPath, slog.Default())
			require.NoError(t, err)
			require.NoError(t, s.Close())

			eventID := clock.MustNewEventID()
			killDelay := time.Duration((i*7)%100) * time.Millisecond

			cmd := exec.Command(os.Args[0],
				"-test.run=TestApply_AtomicityUnderKill",
				"-test.v=false",
				"-test.timeout=30s",
			)
			cmd.Env = append(os.Environ(),
				"MTIX_APPLY_CHAOS_CHILD=1",
				"MTIX_APPLY_CHAOS_DB="+dbPath,
				"MTIX_APPLY_CHAOS_EVENT_ID="+eventID,
				"MTIX_APPLY_CHAOS_NODE_ID=MTIX-"+strconv.Itoa(i+1),
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

			nodeID := "MTIX-" + strconv.Itoa(i+1)
			var nodes int
			require.NoError(t, raw.QueryRow(
				`SELECT COUNT(*) FROM nodes WHERE id = ?`, nodeID,
			).Scan(&nodes))

			var applied int
			require.NoError(t, raw.QueryRow(
				`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, eventID,
			).Scan(&applied))

			// Atomicity invariant: nodes==applied (both 1 if committed,
			// both 0 if rolled back). Never one without the other.
			require.Equalf(t, nodes, applied,
				"atomicity violated: nodes=%d applied=%d (delay=%v)",
				nodes, applied, killDelay)
		})
	}
}

// runApplyChaosChild executes one IdempotentApply call inside a child
// process. The parent SIGKILLs us mid-flight; whichever side of the
// COMMIT we're on determines the post-recovery atomicity assertion.
func runApplyChaosChild(t *testing.T) {
	t.Helper()
	dbPath := os.Getenv("MTIX_APPLY_CHAOS_DB")
	eventID := os.Getenv("MTIX_APPLY_CHAOS_EVENT_ID")
	nodeID := os.Getenv("MTIX_APPLY_CHAOS_NODE_ID")
	if dbPath == "" || eventID == "" || nodeID == "" {
		return
	}
	s, err := sqlite.New(dbPath, slog.Default())
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = s.Close() }()

	pl, err := model.EncodePayload(&model.CreateNodePayload{Title: "chaos-" + nodeID})
	if err != nil {
		os.Exit(3)
	}
	event := &model.SyncEvent{
		EventID:           eventID,
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            model.OpCreateNode,
		Payload:           pl,
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
		SyncStatus:        model.SyncStatusApplied,
	}
	err = s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, event)
	})
	if err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

// TestApply_AtomicityInProcessRollback is the in-process companion
// to the SIGKILL test. Runs N concurrent applies where some are
// designed to fail (apply update on missing node), and verifies the
// final state's nodes-vs-applied count invariant globally.
func TestApply_AtomicityInProcessRollback(t *testing.T) {
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
			pl, _ := model.EncodePayload(&model.CreateNodePayload{Title: "n"})
			nodeID := "MTIX-" + strconv.Itoa(i+1)
			if i%2 == 0 {
				// Half: apply update on missing node — must fail and roll back.
				pl, _ = model.EncodePayload(&model.UpdateFieldPayload{
					FieldName: "title", NewValue: json.RawMessage(`"x"`),
				})
				e := &model.SyncEvent{
					EventID:           clock.MustNewEventID(),
					ProjectPrefix:     "MTIX",
					NodeID:            "GHOST-" + strconv.Itoa(i),
					OpType:            model.OpUpdateField,
					Payload:           pl,
					WallClockTS:       time.Now().UnixMilli(),
					LamportClock:      int64(i + 1),
					VectorClock:       model.VectorClock{"alice": int64(i + 1)},
					AuthorID:          "alice",
					AuthorMachineHash: "0123456789abcdef",
				}
				_ = s.WithTx(context.Background(), func(tx *sql.Tx) error {
					return sqlite.IdempotentApply(context.Background(), tx, e)
				})
				return
			}
			// Other half: valid create — must succeed.
			e := &model.SyncEvent{
				EventID:           clock.MustNewEventID(),
				ProjectPrefix:     "MTIX",
				NodeID:            nodeID,
				OpType:            model.OpCreateNode,
				Payload:           pl,
				WallClockTS:       time.Now().UnixMilli(),
				LamportClock:      int64(i + 1),
				VectorClock:       model.VectorClock{"alice": int64(i + 1)},
				AuthorID:          "alice",
				AuthorMachineHash: "0123456789abcdef",
			}
			_ = s.WithTx(context.Background(), func(tx *sql.Tx) error {
				return sqlite.IdempotentApply(context.Background(), tx, e)
			})
		}(i)
	}
	wg.Wait()

	var nodes int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&nodes))

	var applied int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM applied_events`).Scan(&applied))

	require.Equal(t, nodes, applied,
		"every successfully-applied event MUST have a corresponding nodes row; "+
			"failed applies leave neither — got nodes=%d applied=%d", nodes, applied)
}

// --- Edge cases not covered by the per-op or property tests ---

func TestApply_ReplayAgainstFreshDB(t *testing.T) {
	// 10 events applied against a fresh DB. Mix of creates and
	// updates. Updates on missing nodes surface ErrNotFound; creates
	// land cleanly.
	dbPath := filepath.Join(t.TempDir(), "fresh-replay.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	creates := 0
	for i := 0; i < 10; i++ {
		var op model.OpType
		var payload any
		nodeID := "MTIX-" + strconv.Itoa(i+1)
		if i%3 == 0 {
			op = model.OpUpdateField
			payload = &model.UpdateFieldPayload{
				FieldName: "title", NewValue: json.RawMessage(`"updated"`),
			}
		} else {
			op = model.OpCreateNode
			payload = &model.CreateNodePayload{Title: nodeID}
			creates++
		}
		pl, _ := model.EncodePayload(payload)
		e := &model.SyncEvent{
			EventID:           clock.MustNewEventID(),
			ProjectPrefix:     "MTIX",
			NodeID:            nodeID,
			OpType:            op,
			Payload:           pl,
			WallClockTS:       time.Now().UnixMilli(),
			LamportClock:      int64(i + 1),
			VectorClock:       model.VectorClock{"alice": int64(i + 1)},
			AuthorID:          "alice",
			AuthorMachineHash: "0123456789abcdef",
		}
		_ = s.WithTx(context.Background(), func(tx *sql.Tx) error {
			return sqlite.IdempotentApply(context.Background(), tx, e)
		})
	}

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()

	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&n))
	require.Equal(t, creates, n,
		"only the create events landed; updates on missing nodes were rejected")
}

func TestApply_DeleteThenCreate_NewIDProceeds(t *testing.T) {
	// SYNC-DESIGN section 8.3: tombstones are monotonic for the SAME
	// id. A different-ID create after a delete proceeds normally.
	dbPath := filepath.Join(t.TempDir(), "delete-create.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	pl1, _ := model.EncodePayload(&model.CreateNodePayload{Title: "first"})
	create1 := &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           pl1,
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, create1)
	}))

	pl2, _ := model.EncodePayload(&model.DeletePayload{})
	del := &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpDelete,
		Payload:           pl2,
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      2,
		VectorClock:       model.VectorClock{"alice": 2},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, del)
	}))

	pl3, _ := model.EncodePayload(&model.CreateNodePayload{Title: "second"})
	create2 := &model.SyncEvent{
		EventID:           clock.MustNewEventID(),
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-2",
		OpType:            model.OpCreateNode,
		Payload:           pl3,
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      3,
		VectorClock:       model.VectorClock{"alice": 3},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, create2)
	}))

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()

	var live int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL`,
	).Scan(&live))
	require.Equal(t, 1, live, "MTIX-2 alive; MTIX-1 tombstoned")
}
