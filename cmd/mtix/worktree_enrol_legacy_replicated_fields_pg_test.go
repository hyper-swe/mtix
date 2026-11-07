// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Both fields survive a current push and rerun without comparison exemptions.
func TestEnrolWorktree_RealHub_AddressedCommentAndRerun_Enrols_PG(t *testing.T) {
	binary := realMtix(t)
	for _, history := range []string{"", historyConfig} {
		t.Run(map[bool]string{false: "history off", true: "history on"}[history != ""], func(t *testing.T) {
			f := newEnrolPGFixture(t)
			prepareReplicatedEnrolRerun(t, f, binary, history, false)
			before := enrolRerunChild(t, f)
			require.NotNil(t, before.ClosedAt)
			require.Equal(t, model.StatusOpen, before.Status)
			require.Len(t, before.Annotations, 1)
			require.Equal(t, "lane-b", before.Annotations[0].Addressee)

			result, err := f.enrol()

			require.NoError(t, err)
			require.Len(t, f.enrolled, 1)
			assert.Nil(t, result.LostLocal)
			after := enrolRerunChild(t, f)
			assert.Equal(t, before.ClosedAt, after.ClosedAt)
			assert.Equal(t, before.Annotations, after.Annotations)
		})
	}
}

// An old backfill omitted to, and old workflow events omitted closed_at. The
// original journal and the real hub carry the same old payloads. The refusal's
// printed backup runs; explicit acceptance retains the original values and
// reports their loss from the enrolled clone, including the doctor record.
func TestEnrolWorktree_RealHub_LegacyReplicatedFields_WorkingRecovery_PG(t *testing.T) {
	binary := realMtix(t)
	for _, history := range []string{"", historyConfig} {
		t.Run(map[bool]string{false: "history off", true: "history on"}[history != ""], func(t *testing.T) {
			var notice bytes.Buffer
			f := newEnrolPGFixture(t)
			f.notice = &notice
			prepareReplicatedEnrolRerun(t, f, binary, history, true)
			before := enrolRerunChild(t, f)
			require.NotNil(t, before.ClosedAt)
			refusal := f.refusalOf(t)
			require.Equal(t, service.BoardHubDiffers, refusal.Reason)
			assert.Contains(t, refusal.Diff["TEST-1.1"], "annotations")
			assert.Contains(t, refusal.Diff["TEST-1.1"], "closed_at")
			assert.Empty(t, f.enrolled)
			assert.Contains(t, refusal.Error(), "a value the store already holds cannot be re-entered")
			assert.Contains(t, refusal.Error(), "accept-hub "+refusal.Digest())
			copyPath := runReplicatedEnrolBackup(t, f, binary, history, refusal.Error())
			assertLegacyEnrolCopy(t, copyPath, before)

			result, err := f.service().EnrolWorktree(t.Context(), service.EnrollmentRequest{
				Path: f.root, Mode: "yes", Source: "test", AcceptHub: refusal.Digest(),
			})

			require.NoError(t, err)
			assertLegacyEnrolRecovery(t, f, before, result, notice.String())
		})
	}
}

func prepareReplicatedEnrolRerun(t *testing.T, f *enrolPGFixture, binary, history string, legacy bool) {
	t.Helper()
	f.ownWork(t, func(ctx context.Context, st *sqlite.Store) {
		require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1.1", "TEST-1", 1, 1, "commented child")))
		require.NoError(t, st.AppendAnnotation(ctx, "TEST-1.1", model.Annotation{
			ID: "addressed", Author: "other", Text: "for you", Addressee: "lane-b", CreatedAt: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
		}))
		require.NoError(t, st.ClaimNode(ctx, "TEST-1.1", "lane-a"))
		require.NoError(t, st.TransitionStatus(ctx, "TEST-1.1", model.StatusInProgress, "start", "lane-a"))
		require.NoError(t, st.TransitionStatus(ctx, "TEST-1.1", model.StatusDone, "finished", "lane-a"))
		if legacy {
			makePendingEnrolEventsLegacy(t, st)
		}
	})
	f.binaryLine(t, binary, history, "mtix rerun TEST-1 --strategy all --reason again")
	if legacy {
		st := f.open(t)
		makePendingEnrolEventsLegacy(t, st)
		require.NoError(t, st.Close())
	}
	f.binaryLine(t, binary, history, "mtix sync push --insecure-tls")
	f.writeBoardAs(t, history)
}

func makePendingEnrolEventsLegacy(t *testing.T, st *sqlite.Store) {
	t.Helper()
	events, err := st.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	for _, event := range preReplicatedFieldsEvents(t, events) {
		if event.OpType == model.OpComment {
			// Exact old backfill: node creator, original text/time, no to or snapshot.
			event.Payload, err = model.EncodePayload(&model.CommentPayload{AuthorID: "other", Body: "for you"})
			require.NoError(t, err)
			event.WallClockTS = time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC).UnixMilli()
		}
		_, err = st.WriteDB().ExecContext(t.Context(), `UPDATE sync_events SET payload = ?, wall_clock_ts = ? WHERE event_id = ?`, string(event.Payload), event.WallClockTS, event.EventID)
		require.NoError(t, err)
	}
}

func enrolRerunChild(t *testing.T, f *enrolPGFixture) *model.Node {
	t.Helper()
	st := f.open(t)
	node, err := st.GetNode(t.Context(), "TEST-1.1")
	require.NoError(t, err)
	require.NoError(t, st.Close())
	return node
}

// Preserve history.export while executing the refusal's exact shell command.
func runReplicatedEnrolBackup(t *testing.T, f *enrolPGFixture, binary, history, message string) string {
	t.Helper()
	m := printedBackup.FindStringSubmatch(message)
	require.Len(t, m, 2, "the refusal prints a named backup: %s", message)
	f.binaryLine(t, binary, history, m[1])
	copies, err := filepath.Glob(filepath.Join(f.root, ".mtix", "enrol-copy-*.db"))
	require.NoError(t, err)
	require.Len(t, copies, 1)
	return copies[0]
}

func assertLegacyEnrolCopy(t *testing.T, path string, before *model.Node) {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // G304: the test's printed backup
	require.NoError(t, err)
	assert.Contains(t, string(body), "lane-b")
	assert.Contains(t, string(body), "for you")
	assert.Contains(t, string(body), before.ClosedAt.UTC().Format(time.RFC3339))
}

func assertLegacyEnrolRecovery(t *testing.T, f *enrolPGFixture, before *model.Node, result *service.WorktreeEnrolResult, notice string) {
	t.Helper()
	require.Len(t, f.enrolled, 1)
	aside, err := sqlite.New(result.AsideDir, slog.Default())
	require.NoError(t, err)
	kept, err := aside.GetNode(t.Context(), "TEST-1.1")
	require.NoError(t, err)
	require.NoError(t, aside.Close())
	assert.Equal(t, before.Annotations, kept.Annotations)
	assert.Equal(t, before.ClosedAt, kept.ClosedAt)
	assertLegacyEnrolCopy(t, result.BackupPath, before)
	clone := enrolRerunChild(t, f)
	require.Len(t, clone.Annotations, 1)
	assert.Equal(t, "for you", clone.Annotations[0].Text)
	assert.Empty(t, clone.Annotations[0].Addressee)
	assert.Nil(t, clone.ClosedAt)
	assert.Equal(t, before.Status, clone.Status)
	require.NotNil(t, result.LostLocal)
	assert.Contains(t, result.LostLocal.Diff["TEST-1.1"], "annotations")
	assert.Contains(t, result.LostLocal.Diff["TEST-1.1"], "closed_at")
	assert.Contains(t, notice, "TEST-1.1")
	assert.Contains(t, notice, "annotations")
	assert.Contains(t, notice, "closed_at")
	assert.Contains(t, notice, result.AsideDir)
	assert.FileExists(t, result.LostLocalReport)
	record, err := service.ReadHubAccepted(filepath.Join(f.root, ".mtix"))
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, result.LostLocalReport, record.LostLocalReport)
}
