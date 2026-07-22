// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// MTIX-21: cloud e2e coverage for four FR-18 CLI paths that previously had only
// unit/SQLite-level coverage and were NEVER exercised end-to-end against a real
// hub: sync daemon (sustained pull), sync backup (pg_dump via pooler), sync
// conflicts resolve (LWW round-trip), and sync reconcile. These call the real
// run* command functions against MTIX_PG_TEST_DSN, reusing the PG harness from
// sync_loops_pg_test.go (requireCmdPG / openCmdHub / freshCmdHub). They are
// named TestCloudPath_* so the cloud-contract gate's cmd/mtix -run filter picks
// them up on both the Neon and Supabase shards.

// cloudOpts mirrors the e2e/cmd harness transport options. InsecureTLS skips
// certificate verification for the in-process pgx path: providers like
// Supabase's session pooler serve a private-CA cert that CI verifies via an
// inline sslrootcert, so a laptop run needs no CA wiring. pg_dump (sync backup)
// does its OWN TLS and does not use this.
var cloudOpts = transport.Options{InsecureTLS: true}

// seedLocal creates n nodes in the local store (each emits a pending create
// event) and returns nothing — IDs auto-assign under the TEST prefix.
func seedLocal(t *testing.T, titles ...string) {
	t.Helper()
	for _, title := range titles {
		require.NoError(t, runCreate(title, "", "", 3, "", "", "", "", ""),
			"seed create %q", title)
	}
}

// pushLocal drains the local pending queue to the hub via the real push command.
func pushLocal(t *testing.T, dsn string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPush(context.Background(), &stdout, &stderr,
		[]string{dsn}, cloudOpts, false), "push to hub: %s", stderr.String())
}

// resetLocalForFreshPull wipes the local applied-event ledger, nodes, and pull
// cursor so a subsequent pull must fetch + re-apply every hub event — modelling
// a fresh consumer / clone target.
func resetLocalForFreshPull(t *testing.T) {
	t.Helper()
	db := app.store.WriteDB()
	for _, stmt := range []string{
		`DELETE FROM applied_events`,
		`DELETE FROM nodes`,
		`UPDATE meta SET value = '0' WHERE key = 'meta.sync.last_pulled_clock'`,
	} {
		_, err := db.ExecContext(context.Background(), stmt)
		require.NoErrorf(t, err, "reset: %s", stmt)
	}
}

// liveNodeCount returns the number of non-deleted local nodes.
func liveNodeCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM nodes WHERE deleted_at IS NULL`).Scan(&n))
	return n
}

// TestCloudPath_Daemon_PullTickAppliesHubEvents proves one daemon pull tick
// applies events that live only on the hub — the core of the periodic-pull loop
// exercised over a real provider connection.
func TestCloudPath_Daemon_PullTickAppliesHubEvents(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t) // drop + migrate a pristine hub
	initTestApp(t)

	seedLocal(t, "daemon-alpha", "daemon-beta")
	pushLocal(t, dsn)

	ctx := context.Background()
	resetLocalForFreshPull(t)
	require.Equal(t, 0, liveNodeCount(t), "precondition: local wiped")

	var stderr bytes.Buffer
	runOneDaemonPull(ctx, &stderr, []string{dsn}, cloudOpts)

	require.Equal(t, 2, liveNodeCount(t),
		"one daemon pull tick must re-apply both hub creates (stderr: %s)", stderr.String())
}

// TestCloudPath_Daemon_SustainedLoopPicksUpLaterEvents proves the sustained
// ticker keeps pulling: events pushed to the hub AFTER the loop starts arrive on
// a later tick — the "sync daemon runs for minutes and stays current" guarantee.
func TestCloudPath_Daemon_SustainedLoopPicksUpLaterEvents(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)

	// One node on the hub before the loop starts.
	seedLocal(t, "before-loop")
	pushLocal(t, dsn)
	ctx := context.Background()
	resetLocalForFreshPull(t)

	loopCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runSyncDaemon(loopCtx, &stdout, &stderr, []string{dsn}, cloudOpts, 1, false)
	}()

	// After the immediate pull has had time to land the first node, push a
	// second node so a LATER tick must pick it up.
	time.Sleep(1500 * time.Millisecond)
	seedLocal(t, "after-loop-start")
	pushLocal(t, dsn)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("daemon did not shut down after ctx cancel")
	}

	require.Equal(t, 2, liveNodeCount(t),
		"sustained daemon must have pulled both the pre-loop and mid-loop nodes (stderr: %s)", stderr.String())
	require.Contains(t, stdout.String(), "started")
	require.Contains(t, stdout.String(), "shutting down")

	// The daemon removes its PID file on clean exit.
	_, statErr := os.Stat(filepath.Join(app.mtixDir, daemonPIDFilename))
	require.True(t, os.IsNotExist(statErr), "daemon must remove its PID file on exit")
}

// TestCloudPath_Daemon_SurvivesPullError confirms a pull failure is logged and
// swallowed — the daemon must survive a transient hub outage (e.g. a serverless
// provider cold-start) rather than crash the loop. No hub needed: an
// unreachable DSN forces the error path deterministically.
func TestCloudPath_Daemon_SurvivesPullError(t *testing.T) {
	initTestApp(t)
	var stderr bytes.Buffer
	runOneDaemonPull(context.Background(), &stderr,
		[]string{"postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"}, cloudOpts)
	require.Contains(t, stderr.String(), "pull error",
		"a failed pull must be logged (and swallowed) so the daemon keeps ticking")
}

// strptr returns a pointer to s, for building a store.NodeUpdate.
func strptr(s string) *string { return &s }

// pushRemoteTitleUpdate crafts a well-formed update_field(title) event authored
// by a DIFFERENT identity and pushes it straight to the hub via the transport
// pool (bypassing the local store). Its high Lamport makes it deterministically
// win LWW. Uid-less, so apply scopes the LWW history by node_id and finds any
// local prior on the same field — the setup a manual-resolution test needs.
func pushRemoteTitleUpdate(t *testing.T, pool *transport.Pool, nodeID, newTitle string) {
	t.Helper()
	eid, err := clock.NewEventID()
	require.NoError(t, err)
	payload, err := model.EncodePayload(model.UpdateFieldPayload{
		FieldName: "title",
		NewValue:  json.RawMessage(strconv.Quote(newTitle)),
	})
	require.NoError(t, err)
	ev := &model.SyncEvent{
		EventID:           eid,
		ProjectPrefix:     "TEST",
		NodeID:            nodeID,
		OpType:            model.OpUpdateField,
		Payload:           payload,
		WallClockTS:       time.Now().UTC().UnixMilli(),
		LamportClock:      1_000_000, // dominates any local prior → incoming wins LWW
		VectorClock:       model.VectorClock{"remote-author": 1},
		AuthorID:          "remote-author",
		AuthorMachineHash: "ffffffffffffffff",
		CreatedAt:         time.Now().UTC(),
	}
	accepted, _, err := pool.PushEvents(context.Background(), []*model.SyncEvent{ev})
	require.NoError(t, err)
	require.Len(t, accepted, 1, "hub must accept the remote update event")
}

// conflictRowCount returns how many sync_conflicts rows for nodeID carry the
// given resolution ('lww' for the recorded LWW loss, 'manual' for a user resolve).
func conflictRowCount(t *testing.T, nodeID, resolution string) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_conflicts WHERE node_id = ? AND resolution = ?`,
		nodeID, resolution).Scan(&n))
	return n
}

// TestCloudPath_ConflictsResolve_LWWRoundTripThenManualResolution drives the
// full FR-18.7 conflict lifecycle over a real hub: two authors edit the same
// field, the conflicting event round-trips through the provider, pull records a
// local LWW conflict, and `sync conflicts resolve` appends the manual choice.
func TestCloudPath_ConflictsResolve_LWWRoundTripThenManualResolution(t *testing.T) {
	dsn := requireCmdPG(t)
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()

	// A node exists on the hub.
	seedLocal(t, "conflict-node")
	pushLocal(t, dsn)

	// This CLI edits the title locally (the LWW "prior"; stays pending).
	require.NoError(t, app.store.UpdateNode(ctx, "TEST-1",
		&store.NodeUpdate{Title: strptr("local-edit")}))

	// A different author's title edit lands on the hub, concurrent with ours.
	pushRemoteTitleUpdate(t, pool, "TEST-1", "remote-edit")

	// Pull applies the remote edit; LWW sees our prior → records an 'lww' row.
	var out, errb bytes.Buffer
	require.NoError(t, runSyncPull(ctx, &out, &errb, []string{dsn}, cloudOpts, 100),
		"pull: %s", errb.String())
	require.Equal(t, 1, conflictRowCount(t, "TEST-1", "lww"),
		"pull must have recorded exactly one LWW conflict for the contested title")

	// Find the conflict id and resolve it — appends a 'manual' resolution row.
	var cid int64
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT conflict_id FROM sync_conflicts WHERE node_id = ? AND resolution = 'lww' ORDER BY conflict_id LIMIT 1`,
		"TEST-1").Scan(&cid))

	out.Reset()
	errb.Reset()
	require.NoError(t, runSyncConflictsResolve(ctx, &out, &errb,
		strconv.FormatInt(cid, 10), "keep-local"))
	require.Contains(t, out.String(), "recorded manual resolution")
	require.Equal(t, 1, conflictRowCount(t, "TEST-1", "manual"),
		"resolve must have appended exactly one manual resolution row")
}

// prefixNodeCount counts non-deleted local nodes whose display id is under prefix.
func prefixNodeCount(t *testing.T, prefix string) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM nodes WHERE id LIKE ? AND deleted_at IS NULL`,
		prefix+"-%").Scan(&n))
	return n
}

// childCount counts non-deleted local children of parentID.
func childCount(t *testing.T, parentID string) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM nodes WHERE parent_id = ? AND deleted_at IS NULL`,
		parentID).Scan(&n))
	return n
}

// TestCloudPath_Reconcile_DiscardLocal_TakesHubState resolves a divergence by
// discarding local state and re-cloning the hub — the "--discard-local then
// converge on the hub" path, exercised against a real populated hub.
func TestCloudPath_Reconcile_DiscardLocal_TakesHubState(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()

	seedLocal(t, "on-hub") // TEST-1
	pushLocal(t, dsn)
	seedLocal(t, "local-only-divergent") // TEST-2, never pushed
	require.Equal(t, 2, liveNodeCount(t))

	var out, errb bytes.Buffer
	require.NoError(t, runSyncReconcile(ctx, &out, &errb,
		reconcileFlags{discardLocal: true, yes: true}))
	require.Contains(t, out.String(), "discard-local complete")
	require.Equal(t, 0, liveNodeCount(t), "discard-local clears local nodes")

	// Re-clone from the hub → converge on hub state: TEST-1 only, divergent
	// TEST-2 gone.
	out.Reset()
	errb.Reset()
	require.NoError(t, runSyncClone(ctx, &out, &errb, []string{dsn}, cloudOpts, false, 100),
		"clone after discard: %s", errb.String())
	require.Equal(t, 1, liveNodeCount(t),
		"after discard + clone the local store matches the hub")
}

// TestCloudPath_Reconcile_RenameTo_RewritesAndPushes resolves a prefix
// collision: rewrite local ids to a fresh prefix, then push cleanly to the hub.
func TestCloudPath_Reconcile_RenameTo_RewritesAndPushes(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()

	seedLocal(t, "r-alpha", "r-beta") // TEST-1, TEST-2

	var out, errb bytes.Buffer
	require.NoError(t, runSyncReconcile(ctx, &out, &errb,
		reconcileFlags{renameTo: "RENAMED", yes: true}))
	require.Contains(t, out.String(), "rename-to RENAMED complete")

	require.Equal(t, 2, prefixNodeCount(t, "RENAMED"), "local ids moved to the new prefix")
	require.Equal(t, 0, prefixNodeCount(t, "TEST"), "the old prefix is vacated")

	// The renamed tree pushes cleanly (fresh namespace, no hub collision).
	pushLocal(t, dsn)
}

// TestCloudPath_Reconcile_ImportAs_ReparentsUnderHubParent resolves a divergence
// by re-parenting the local tree under a node that already exists on the hub,
// then pushing the re-parented tree.
func TestCloudPath_Reconcile_ImportAs_ReparentsUnderHubParent(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()

	seedLocal(t, "parent") // TEST-1
	pushLocal(t, dsn)
	seedLocal(t, "to-reparent") // TEST-2, a local root to move under TEST-1
	require.Equal(t, 0, childCount(t, "TEST-1"), "precondition: parent has no children")

	var out, errb bytes.Buffer
	require.NoError(t, runSyncReconcile(ctx, &out, &errb,
		reconcileFlags{importAs: "TEST-1", yes: true}))
	require.Contains(t, out.String(), "import-as TEST-1 complete")

	require.GreaterOrEqual(t, childCount(t, "TEST-1"), 1,
		"the local root was re-parented under the hub node")
	// The re-parented tree pushes cleanly.
	pushLocal(t, dsn)
}

var pgMajorRE = regexp.MustCompile(`(\d+)`)

// pgDumpTrustDSN returns a DSN pg_dump can use non-interactively. When the
// operator's DSN requests certificate verification (sslmode=verify-ca/
// verify-full) but names no sslrootcert, libpq's pg_dump looks for
// ~/.postgresql/root.crt and aborts when it is absent. We point it at the OS
// trust store (sslrootcert=system, libpq >= 16) so a public-CA provider like
// Neon verifies; a private-CA provider like Supabase supplies its own
// sslrootcert inline upstream (the MTIX-42 gate builds it that way), which this
// leaves untouched. This mirrors correct operator DSN configuration — mtix sync
// backup passes the DSN to pg_dump verbatim, so the SSL trust root is the
// operator's to supply (see MTIX-21 notes: candidate follow-up to have backup
// default this).
func pgDumpTrustDSN(dsn string) string {
	low := strings.ToLower(dsn)
	if !strings.Contains(low, "sslmode=verify") || strings.Contains(low, "sslrootcert=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "sslrootcert=system"
}

// serverMajor returns the hub's PostgreSQL major version. InsecureSkipVerify so
// it works for providers (Supabase pooler) that serve a private/non-standard
// cert, matching the transport path the other cloud tests use.
func serverMajor(t *testing.T, dsn string) (int, bool) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return 0, false
	}
	if cfg.ConnConfig.TLSConfig != nil {
		cfg.ConnConfig.TLSConfig.InsecureSkipVerify = true //nolint:gosec // test-only version probe
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return 0, false
	}
	defer pool.Close()
	var ver string
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&ver); err != nil {
		return 0, false
	}
	m := pgMajorRE.FindString(ver)
	n, err := strconv.Atoi(m)
	if err != nil {
		return 0, false
	}
	return n, true
}

// requirePgDumpForServer skips the calling test unless a pg_dump is available
// whose major version is >= the hub's — pg_dump refuses to dump a newer server.
// This keeps `go test ./...` green on a laptop without PG client tools while the
// cloud gate (which installs a matching client) runs it for real.
func requirePgDumpForServer(t *testing.T, dsn string) {
	t.Helper()
	bin := pgDumpBin()
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("pg_dump (%q) not on PATH; set MTIX_PG_DUMP to a client >= the server major to run the backup cloud test", bin)
	}
	out, err := exec.Command(path, "--version").CombinedOutput() //nolint:gosec // resolved from pgDumpBin/LookPath
	require.NoError(t, err, "pg_dump --version")
	dumpMajor, err := strconv.Atoi(pgMajorRE.FindString(string(out)))
	require.NoError(t, err, "parse pg_dump version from %q", string(out))

	serverMaj, ok := serverMajor(t, dsn)
	if !ok {
		t.Skip("could not determine hub server version to gate pg_dump compatibility")
	}
	if dumpMajor < serverMaj {
		t.Skipf("pg_dump major %d < server major %d — pg_dump refuses a newer server; install a client >= %d (e.g. libpq) and set MTIX_PG_DUMP",
			dumpMajor, serverMaj, serverMaj)
	}
}

// TestCloudPath_Backup_DumpsHubTablesThroughDSN runs `mtix sync backup` (pg_dump)
// against the hub DSN — the pooler/pg_dump round-trip (version negotiation, COPY
// through a session pooler) that FR-18.21 relies on and that had no live
// coverage. Asserts the dump defines every mtix-owned table and carries the
// seeded event data.
func TestCloudPath_Backup_DumpsHubTablesThroughDSN(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()

	seedLocal(t, "backup-node")
	pushLocal(t, dsn)

	requirePgDumpForServer(t, dsn)

	out := filepath.Join(t.TempDir(), "hub-backup.sql")
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncBackup(ctx, &stdout, &stderr, []string{pgDumpTrustDSN(dsn)}, out),
		"backup: %s", stderr.String())
	require.Contains(t, stdout.String(), "backup written to")

	body, err := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
	require.NoError(t, err)
	dump := string(body)
	for _, tbl := range backupTables {
		require.Contains(t, dump, tbl, "dump must reference mtix-owned table %s", tbl)
	}
	require.Contains(t, dump, "backup-node",
		"dump must contain the seeded node's create-event payload (data, not just schema)")
}
