// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Micro uses the same gap-free rejected-create boundary as create (MTIX-106).
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestMicro_TerminalParent_DoesNotConsumeSequence(t *testing.T) {
	for _, status := range []model.Status{model.StatusDone, model.StatusCancelled, model.StatusInvalidated} {
		t.Run(string(status), func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			parent, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "Parent"})
			require.NoError(t, err)
			_, err = app.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET status = ? WHERE id = ?`, status, parent.ID)
			require.NoError(t, err)
			cmd := newMicroCmd()
			cmd.SetArgs([]string{"Refused micro", "--under", parent.ID})
			require.ErrorIs(t, cmd.Execute(), model.ErrInvalidInput)
			var count int
			require.NoError(t, app.store.QueryRow(ctx, `SELECT COUNT(*) FROM sequences WHERE key = ?`, "TEST:"+parent.ID).Scan(&count))
			assert.Zero(t, count)
			_, err = app.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET status = ? WHERE id = ?`, model.StatusOpen, parent.ID)
			require.NoError(t, err)
			cmd = newMicroCmd()
			cmd.SetArgs([]string{"Accepted micro", "--under", parent.ID})
			require.NoError(t, cmd.Execute())
			node, err := app.store.GetNode(ctx, parent.ID+".1")
			require.NoError(t, err)
			assert.Equal(t, "Accepted micro", node.Title)
		})
	}
}
