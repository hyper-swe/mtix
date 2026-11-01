// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestAppendUniqueUIDCheck_NoStore_Fails verifies the doctor's unique-uid
// check fails like the other local checks when there is no store.
func TestAppendUniqueUIDCheck_NoStore_Fails(t *testing.T) {
	r := appendUniqueUIDCheck(context.Background(), DoctorReport{}, nil)
	require.Len(t, r.Checks, 1)
	assert.Equal(t, uniqueUIDCheckName, r.Checks[0].Name)
	assert.False(t, r.Checks[0].Pass)
}

// TestRunSyncDoctor_DuplicateNodeUIDs_FailsNamingThemAndTheFix verifies the
// doctor passes on a clean store and, when two nodes share a uid, fails
// with the uid, both node ids and the exact recovery (MTIX-95.31.8).
func TestRunSyncDoctor_DuplicateNodeUIDs_FailsNamingThemAndTheFix(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"TEST-1", "TEST-2"} {
		require.NoError(t, app.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "TEST", Seq: i + 1, Title: id, Status: model.StatusOpen,
			Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeEpic,
			ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	run := func() DoctorCheck {
		var out bytes.Buffer
		app.jsonOutput = true
		_ = runSyncDoctor(ctx, &out, nil, transport.Options{})
		return doctorCheckFromJSON(t, out.Bytes(), uniqueUIDCheckName)
	}
	assert.True(t, run().Pass)

	_, err := app.store.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET uid = (SELECT uid FROM nodes WHERE id = 'TEST-1') WHERE id = 'TEST-2'`)
	require.NoError(t, err)
	check := run()
	assert.False(t, check.Pass)
	for _, want := range []string{"TEST-1", "TEST-2", "UPDATE nodes SET uid = ''"} {
		assert.Contains(t, check.Detail, want)
	}
}

// TestRunVerify_DuplicateNodeUIDs_ReportsRecoveryTextAndJSON verifies
// `mtix verify` reports a shared uid with the recovery, in text and with
// --json (uid_unique_ok false, verified false), and passes on a clean store
// (MTIX-95.31.8).
func TestRunVerify_DuplicateNodeUIDs_ReportsRecoveryTextAndJSON(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"TEST-1", "TEST-2"} {
		require.NoError(t, app.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "TEST", Seq: i + 1, Title: id, Status: model.StatusOpen,
			Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeEpic,
			ContentHash: model.ComputeContentHash(id, "", "", "", nil), CreatedAt: created, UpdatedAt: created,
		}))
	}
	clean := captureStdout(t, func() { require.NoError(t, runVerify("")) })
	assert.Contains(t, clean, "All node uids unique")

	_, err := app.store.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET uid = (SELECT uid FROM nodes WHERE id = 'TEST-1') WHERE id = 'TEST-2'`)
	require.NoError(t, err)

	text := captureStdout(t, func() { require.NoError(t, runVerify("")) })
	for _, want := range []string{"INTEGRITY FAILURE", `"TEST-1", "TEST-2"`, "UPDATE nodes SET uid = ''"} {
		assert.Contains(t, text, want)
	}

	app.jsonOutput = true
	raw := captureStdout(t, func() { require.NoError(t, runVerify("")) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &got))
	assert.Equal(t, false, got["uid_unique_ok"])
	assert.Equal(t, false, got["verified"])
	assert.Contains(t, got["uid_unique_recovery"], "UPDATE nodes SET uid = ''")
}

// TestSyncedProjectSignals_AreObservableOnMain pins every signal the
// duplicate-uid recovery text tells an agent to look at to decide whether a
// project syncs (MTIX-95.31.8): the status rows project_prefix (shown as "-" when empty),
// pushed and applied, the MTIX_SYNC_DSN variable and the .mtix/secrets file.
func TestSyncedProjectSignals_AreObservableOnMain(t *testing.T) {
	initTestApp(t)
	assert.Equal(t, "MTIX_SYNC_DSN", transport.EnvDSN)
	assert.Equal(t, "secrets", transport.SecretsFilename)

	var empty, set bytes.Buffer
	require.NoError(t, printStatusTable(&empty, SyncStatus{}))
	require.NoError(t, printStatusTable(&set, SyncStatus{ProjectPrefix: "TEST", Pushed: 2, Applied: 3}))
	assert.Regexp(t, `(?m)^project_prefix\s+-$`, empty.String())
	assert.Regexp(t, `(?m)^project_prefix\s+TEST$`, set.String())
	assert.Regexp(t, `(?m)^pushed\s+2$`, set.String())
	assert.Regexp(t, `(?m)^applied\s+3$`, set.String())

	report := duplicateUIDRecoveryForTest(t)
	for _, want := range []string{"project_prefix", "pushed or applied", "MTIX_SYNC_DSN", ".mtix/secrets",
		"mtix backup .mtix/data/backups/", "MTIX-95.31.8.1"} {
		assert.Contains(t, report, want)
	}
	assert.NotContains(t, report, "reconcile")
	assert.NotContains(t, report, "sync.enabled", "nothing sets it, so it is not a signal")
}

// duplicateUIDRecoveryForTest returns the recovery text for a store with a shared uid.
func duplicateUIDRecoveryForTest(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"TEST-1", "TEST-2"} {
		require.NoError(t, app.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "TEST", Seq: i + 1, Title: id, Status: model.StatusOpen,
			Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeEpic,
			ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	_, err := app.store.WriteDB().ExecContext(ctx, `DROP INDEX idx_nodes_uid`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET uid = (SELECT uid FROM nodes WHERE id = 'TEST-1') WHERE id = 'TEST-2'`)
	require.NoError(t, err)
	report, err := app.store.DuplicateNodeUIDsReport(ctx)
	require.NoError(t, err)
	return report
}

// TestSyncDoctorHelp_ListsTheUniqueUIDCheck verifies the doctor's help states
// the number of checks and lists the unique node uids check.
func TestSyncDoctorHelp_ListsTheUniqueUIDCheck(t *testing.T) {
	long := newSyncDoctorCmd().Long
	assert.Contains(t, long, "Run health checks")
	assert.Contains(t, long, "Unique node uids")
}

// TestRunSyncDoctor_HostileHolderIDs checks JSON detail and the human doctor output.
func TestRunSyncDoctor_HostileHolderIDs(t *testing.T) {
	for _, fixture := range testutil.HostileIDs() {
		t.Run(fixture.Name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, "")
			ctx := context.Background()
			// A cached peer identity keeps this display test independent of host networking.
			_, err := app.store.WriteDB().ExecContext(ctx, `INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, "meta.sync.machine_hash", "0123456789abcdef")
			require.NoError(t, err)
			duplicateUIDRecoveryForTest(t)
			_, err = app.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET id = ? WHERE id = ?`, fixture.Value, "TEST-1")
			require.NoError(t, err)
			var out bytes.Buffer
			app.jsonOutput = true
			require.Error(t, runSyncDoctor(ctx, &out, nil, transport.Options{}))
			check := doctorCheckFromJSON(t, out.Bytes(), uniqueUIDCheckName)
			assert.False(t, check.Pass)
			assert.NotContains(t, check.Detail, "\n")
			assert.Equal(t, check.Detail, strings.Map(func(r rune) rune {
				if r < 0x20 || r > 0x7e {
					return -1
				}
				return r
			}, check.Detail), "detail must be printable ASCII")
			assert.Contains(t, check.Detail, format.EventID(fixture.Value))
			out.Reset()
			app.jsonOutput = false
			require.Error(t, runSyncDoctor(ctx, &out, nil, transport.Options{}))
			assert.Contains(t, out.String(), check.Detail)
			var stored string
			require.NoError(t, app.store.WriteDB().QueryRowContext(ctx, `SELECT id FROM nodes WHERE id = ?`, fixture.Value).Scan(&stored))
			assert.Equal(t, fixture.Value, stored)
		})
	}
}
