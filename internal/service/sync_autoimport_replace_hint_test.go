// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestAutoImportRefusal_ReplaceOption_SaysTypedConfirmationAndHuman verifies
// that every text of the auto-import refusal that offers
// `mtix import ... --mode replace` says the replace needs the ticket count
// typed at an interactive terminal and is a human's command, so an agent
// reading the refusal does not run it (MTIX-107.74). The command text the
// other tests pin stays in the line.
func TestAutoImportRefusal_ReplaceOption_SaysTypedConfirmationAndHuman(t *testing.T) {
	ctx := context.Background()
	f, board := refusedFixture(t)
	writeLocalTask(t, f, "PROJ-4", 4)

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	refusal := lineWith(f.notices.String(), "mtix import .mtix/tasks.json --mode replace")
	assert.Contains(t, refusal, "typed at an interactive terminal", "refusal option line")
	assert.Contains(t, refusal, "human", "refusal option line")

	f.notices.Reset()
	f.pull(t, board)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	pending := lineWith(f.notices.String(), "--mode replace")
	assert.Contains(t, pending, "typed at an interactive terminal", "pending-write line")
	assert.Contains(t, pending, "human", "pending-write line")
}

// TestAutoImport_LosslessConflictWarning_SaysTypedConfirmationAndHuman pins
// the logged ways out of a conflict that would lose nothing: the replace
// option says it needs the ticket count typed at an interactive terminal and
// that a human runs it (MTIX-107.74).
func TestAutoImport_LosslessConflictWarning_SaysTypedConfirmationAndHuman(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	var logs bytes.Buffer
	f.svc = service.NewSyncService(f.store, slog.New(slog.NewTextHandler(&logs, nil)),
		func() time.Time { return f.now })
	f.svc.SetNoticeWriter(f.notices)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	title := "Local edit after the last export"
	require.NoError(t, f.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Title: &title}))
	f.pull(t, board)

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	warn := lineWith(logs.String(), "--mode replace")
	assert.Contains(t, warn, "typed at an interactive terminal")
	assert.Contains(t, warn, "human")
}
