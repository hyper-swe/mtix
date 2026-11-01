// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pullClientRegisterTimeout bounds the client registration write, so a slow
// hub statement cannot hold up a pull (MTIX-95.13).
const pullClientRegisterTimeout = 5 * time.Second

// clientUpserter is the hub surface of the client registration;
// *transport.Pool implements it.
type clientUpserter interface {
	UpsertProjectClient(ctx context.Context, prefix, machineHash, cliVersion string) error
}

// clientIdentity is who the pulling CLI is: its machine hash and the build
// version it reports to the version gate.
type clientIdentity struct {
	machineHash string
	cliVersion  string
}

// registerPullClient records this client on the hub, with the board's sync
// project prefix (meta.sync.project_prefix, set by sync init), the machine
// hash and the running CLI version, with the same semantics as push and init
// (clients.go upsertProjectClientExec). Before this a client that only
// pulls, a daemon for one, was invisible to the version gate and to any
// stale-client report (MTIX-95.13, review F-12).
//
// It never fails the pull. A board without a sync project prefix or a
// machine without a hash has nothing to register and is skipped quietly. A
// failed write prints one warning that names the grant the pulling role
// needs, and the pull continues: the write is one statement on the
// connection the pull already opened, bounded by pullClientRegisterTimeout.
func registerPullClient(ctx context.Context, stderr io.Writer, hub clientUpserter,
	st *sqlite.Store, id clientIdentity,
) {
	if id.machineHash == "" || id.cliVersion == "" {
		return
	}
	var prefix string
	err := st.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.project_prefix'`).Scan(&prefix)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && prefix == "") {
		return
	}
	if err != nil {
		warnPullClient(stderr, fmt.Errorf("read the sync project prefix: %w", err))
		return
	}
	regCtx, cancel := context.WithTimeout(ctx, pullClientRegisterTimeout)
	defer cancel()
	if err := hub.UpsertProjectClient(regCtx, prefix, id.machineHash, id.cliVersion); err != nil {
		warnPullClient(stderr, err)
	}
}

// warnPullClient prints the one registration warning: the pull is not
// affected, and the grant that fixes it.
func warnPullClient(stderr io.Writer, err error) {
	warnSync(stderr, "WARN: pull client registration skipped (the pull is not affected); "+
		"the hub role this client uses needs INSERT and UPDATE on sync_project_clients", err)
}
