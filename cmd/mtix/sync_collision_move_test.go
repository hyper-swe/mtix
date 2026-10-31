// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestCollisionMove verifies that hub refusal cannot change local state, and
// that a failed local move reports a concrete recovery command.
func TestCollisionMove(t *testing.T) {
	for _, refused := range []bool{true, false} {
		t.Run(map[bool]string{true: "hub refuses", false: "local fails"}[refused], func(t *testing.T) {
			accepted, moved := false, false
			failure := errors.New("write refused")
			resolve := func(context.Context) error {
				if refused {
					return failure
				}
				accepted = true
				return nil
			}
			renumber := func(context.Context, string) (string, error) {
				moved = true
				require.True(t, accepted, "hub must accept before the local move")
				return "", failure
			}
			_, err := resolveCollisionMove(context.Background(), transport.OpenCollision{CollisionID: 42}, "incoming", "loser", collisionMove{accept: resolve, renumber: renumber, finish: func(context.Context, string) error { return nil }})
			require.Error(t, err)
			if refused {
				require.False(t, moved, "refused update must leave local state unchanged")
			} else {
				require.Contains(t, err.Error(), "hub accepted")
				require.Contains(t, err.Error(), "mtix sync collisions resolve 42 --winner incoming")
			}
		})
	}
}

// A real store demonstrates retries both before and after the local commit.
func TestCollisionMove_RetryRenumbersOnce(t *testing.T) {
	for _, failAfterMove := range []bool{false, true} {
		t.Run(map[bool]string{false: "local write fails", true: "hub completion fails"}[failAfterMove], func(t *testing.T) {
			initTestApp(t)
			cacheCollisionMachineHash(t)
			ctx := context.Background()
			require.NoError(t, runCreate("Collision loser", "", "", 3, "", "", "", "", ""))
			uid := localNodeUID(t, app.store, "TEST-1")
			c := transport.OpenCollision{CollisionID: 42, DisplayPath: "TEST-1"}
			fail := true
			cause := errors.New("write refused")
			move := collisionMove{
				accept: func(context.Context) error { return nil },
				renumber: func(ctx context.Context, uid string) (string, error) {
					if fail && !failAfterMove {
						return "", cause
					}
					path, err := renumberCollisionLoser(ctx, uid, c.DisplayPath)
					return path, err
				},
				finish: func(context.Context, string) error {
					if fail && failAfterMove {
						return cause
					}
					return nil
				},
			}
			_, err := resolveCollisionMove(ctx, c, "held", uid, move)
			require.ErrorIs(t, err, cause)
			expected := "hub accepted collision 42 but local renumber failed"
			if failAfterMove {
				expected = "hub accepted collision 42 but recording completion on the hub failed"
			}
			require.ErrorContains(t, err, expected)
			require.ErrorContains(t, err, "fix the cause, then run on the client that owns the losing node")
			require.Contains(t, err.Error(), "mtix sync collisions resolve 42 --winner held")
			fail = false
			for range 2 {
				path, err := resolveCollisionMove(ctx, c, "held", uid, move)
				require.NoError(t, err)
				require.Equal(t, "TEST-2", path)
			}
			node, err := app.store.GetNode(ctx, "TEST-2")
			require.NoError(t, err)
			require.Equal(t, uid, localNodeUID(t, app.store, node.ID))
		})
	}
}

// Avoid consulting network interfaces in sandboxed tests; this is the same
// durable identity cache that the store normally populates on its first write.
func cacheCollisionMachineHash(t *testing.T) {
	t.Helper()
	require.NoError(t, app.store.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`, "0123456789abcdef")
		return err
	}))
}

// Refusal preserves the real task and its pending sync events, not just the
// move callback count. No network is needed for this hub failure injection.
func TestCollisionMove_RefusedHubPreservesStore(t *testing.T) {
	initTestApp(t)
	cacheCollisionMachineHash(t)
	ctx := context.Background()
	require.NoError(t, runCreate("Refused collision loser", "", "", 3, "", "", "", "", ""))
	uid := localNodeUID(t, app.store, "TEST-1")
	before, err := readPendingBatch(ctx, app.store, 100)
	require.NoError(t, err)
	c := transport.OpenCollision{CollisionID: 42, DisplayPath: "TEST-1"}
	refused := errors.New("permission denied for table sync_node_collisions")
	move := collisionMove{
		accept: func(context.Context) error { return refused },
		renumber: func(ctx context.Context, uid string) (string, error) {
			return renumberCollisionLoser(ctx, uid, c.DisplayPath)
		},
		finish: func(context.Context, string) error { t.Fatal("cannot complete a refused decision"); return nil },
	}
	_, err = resolveCollisionMove(ctx, c, "held", uid, move)
	require.ErrorIs(t, err, refused)
	require.Equal(t, uid, localNodeUID(t, app.store, "TEST-1"))
	after, err := readPendingBatch(ctx, app.store, 100)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
