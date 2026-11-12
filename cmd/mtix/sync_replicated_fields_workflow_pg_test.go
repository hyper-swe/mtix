// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestSyncPushClone_WorkflowAfterRerun_ReplicatesStatusAndClosedAt(t *testing.T) {
	for _, name := range []string{"claim", "claim and unclaim", "block and unblock"} {
		t.Run(name, func(t *testing.T) {
			_ = openCmdHub(t)
			dsn := os.Getenv(transport.EnvDSN)
			source, clone := doctorCLIBoard(t, ""), doctorCLIBoard(t, "")
			run := func(root string, args ...string) string {
				t.Helper()
				out := doctorCLIExecute(t, root, dsn, args...)
				require.Zero(t, out.exit, "%s: %s", args, out.stderr)
				return out.stdout
			}
			var parent, child model.Node
			require.NoError(t, json.Unmarshal([]byte(run(source, "create", "parent", "--json")), &parent))
			require.NoError(t, json.Unmarshal([]byte(run(source, "create", "child", "--under", parent.ID, "--json")), &child))
			run(source, "claim", child.ID, "--agent", "worker")
			run(source, "done", child.ID)
			run(source, "rerun", parent.ID, "--strategy=all")
			if name == "block and unblock" {
				var blocker model.Node
				require.NoError(t, json.Unmarshal([]byte(run(source, "create", "blocker", "--json")), &blocker))
				run(source, "dep", "add", blocker.ID, child.ID, "--type=blocks")
				run(source, "dep", "remove", blocker.ID, child.ID, "--type=blocks")
			} else {
				run(source, "claim", child.ID, "--agent", "worker")
				if name == "claim and unclaim" {
					run(source, "unclaim", child.ID, "--reason", "handoff", "--agent", "worker")
				}
			}
			run(source, "sync", "push", "--insecure-tls")
			run(clone, "sync", "clone", "--insecure-tls")
			var want, got model.Node
			require.NoError(t, json.Unmarshal([]byte(run(source, "show", child.ID, "--json")), &want))
			require.NoError(t, json.Unmarshal([]byte(run(clone, "show", child.ID, "--json")), &got))
			require.NotNil(t, want.ClosedAt)
			require.Equal(t, want.Status, got.Status)
			require.Equal(t, want.ClosedAt, got.ClosedAt)
		})
	}
}
