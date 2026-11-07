// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// Stock main pushes a used board and clones its journal into an empty board.
// Compare real fields, without the enrol normalization that hid lost data.
func TestSyncReplicatedFields_RealPushClone_PreservesAnnotationsAndRerun(t *testing.T) {
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
	require.NotEmpty(t, child.ID)
	run(source, "comment", child.ID, "review this", "--to", "receiver")
	var before model.Node
	require.NoError(t, json.Unmarshal([]byte(run(source, "show", child.ID, "--json")), &before))
	require.Len(t, before.Annotations, 1)
	run(source, "resolve-annotation", child.ID, before.Annotations[0].ID)
	run(source, "claim", child.ID, "--agent", "worker")
	run(source, "done", child.ID)
	run(source, "rerun", parent.ID, "--strategy=all")
	run(source, "sync", "push", "--insecure-tls")
	run(clone, "sync", "clone", "--insecure-tls")
	var want, got model.Node
	require.NoError(t, json.Unmarshal([]byte(run(source, "show", child.ID, "--json")), &want))
	require.NoError(t, json.Unmarshal([]byte(run(clone, "show", child.ID, "--json")), &got))
	require.Len(t, want.Annotations, 1)
	require.Equal(t, "receiver", want.Annotations[0].Addressee)
	require.True(t, want.Annotations[0].Resolved)
	require.NotNil(t, want.ClosedAt, "rerun retains the source completion time")
	require.Equal(t, want.Annotations, got.Annotations)
	require.Equal(t, want.ClosedAt, got.ClosedAt)
	require.Equal(t, want.Status, got.Status)
	for _, root := range []string{source, clone} {
		var exported struct {
			Nodes []struct {
				ID          string             `json:"id"`
				Annotations []model.Annotation `json:"annotations"`
				ClosedAt    *time.Time         `json:"closed_at"`
			} `json:"nodes"`
		}
		require.NoError(t, json.Unmarshal([]byte(run(root, "export")), &exported))
		var found bool
		for _, n := range exported.Nodes {
			if n.ID == child.ID {
				found = true
				require.Equal(t, want.Annotations, n.Annotations)
				require.Equal(t, want.ClosedAt, n.ClosedAt)
			}
		}
		require.True(t, found)
	}
}
