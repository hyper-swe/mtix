// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// collectHubEvidence gathers the local hub evidence the sticky mark accepts
// besides stored state: sync or relay keys in config and a DSN in the
// secrets file (ADR-007 §3.5). An exported MTIX_SYNC_DSN is not evidence
// until a command connects with it, so EnvDSNConnected stays false here.
// The secrets content is only tested for presence, never read out.
func collectHubEvidence() service.HubEvidence {
	ev := service.HubEvidence{ConfigKeys: relayDir() != ""}
	if app.configSvc != nil {
		for _, key := range []string{"sync.enabled", "sync.endpoint"} {
			if v, err := app.configSvc.Get(key); err == nil && v != "" && v != "false" {
				ev.ConfigKeys = true
			}
		}
	}
	if app.mtixDir != "" {
		body, _, err := transport.ReadSecretsFile(app.mtixDir)
		ev.SecretsDSN = err == nil && strings.TrimSpace(body) != ""
	}
	return ev
}

// noteHubConnection records that a sync or relay command reached a hub for
// this board, then brings the operator-local mirror up to date. It is best effort: a failure
// is reported on stderr and never fails the sync that already succeeded.
func noteHubConnection(ctx context.Context, stderr io.Writer) {
	if app.store == nil || app.mtixDir == "" {
		return
	}
	svc := service.NewHubMarkService(app.store)
	var err error
	if os.Getenv(transport.EnvDSN) != "" {
		_, err = svc.RecordEnvConnection(ctx)
	} else {
		_, err = svc.HubConnected(ctx, service.HubEvidence{SecretsDSN: true})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not record the hub-connected mark: %v\n", err)
		return
	}
	// Whatever flipped the mark (this call, a foreign event applied earlier,
	// an inference), the operator-local mirror follows the store.
	if err := reconcileHubMarkMirror(); err != nil {
		fmt.Fprintf(stderr, "warning: could not mirror the hub-connected mark: %v\n", err)
	}
}

// reconcileHubMarkMirror makes the operator-local mirror equal the store's
// mark: it writes the source when the store is marked and the mirror differs,
// and clears the mirror when the store is not marked. The store is the
// authority, so the two cannot drift (ADR-007 §3.5).
func reconcileHubMarkMirror() error {
	if app.store == nil || app.mtixDir == "" {
		return nil
	}
	mark, err := app.store.HubMark(context.Background())
	if err != nil {
		return fmt.Errorf("reconcile hub mark mirror: %w", err)
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return fmt.Errorf("reconcile hub mark mirror: %w", err)
	}
	state := oplocal.New(env)
	checkout := filepath.Clean(filepath.Dir(app.mtixDir))
	mirror, _, err := state.HubMarkMirror(checkout)
	if err != nil {
		return fmt.Errorf("reconcile hub mark mirror: %w", err)
	}
	switch {
	case mark.Set && mirror.Source != mark.Source:
		return state.MirrorHubMark(checkout, mark.Source)
	case !mark.Set && mirror.Source != "":
		return state.ClearHubMarkMirror(checkout)
	}
	return nil
}

// resetHubMark clears the mark and its operator-local mirror together and
// records the applied_events floor. The caller enforces the interactive-
// terminal rule (the reset command is planned: MTIX-112.19.2).
func resetHubMark(ctx context.Context, stderr io.Writer) error {
	if app.store == nil {
		return fmt.Errorf("reset hub mark: local store not initialized")
	}
	if _, err := service.NewHubMarkService(app.store).ResetHubMark(ctx); err != nil {
		return err
	}
	if err := reconcileHubMarkMirror(); err != nil {
		fmt.Fprintf(stderr, "warning: could not clear the hub-connected mirror: %v\n", err)
	}
	return nil
}

// shouldReconcileHubMark reports whether a command path can change the
// mark through applied events: the sync, relay and daemon commands.
func shouldReconcileHubMark(commandPath string) bool {
	for _, part := range []string{" sync", " relay", " daemon"} {
		if strings.Contains(commandPath, part) {
			return true
		}
	}
	return false
}

// appendHubMarkCheck reports the sticky mark and its source (ADR-007 A9). It
// only reads: inspection never writes the mark.
func appendHubMarkCheck(ctx context.Context, report DoctorReport) DoctorReport {
	if app.store == nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark", Pass: true, Warn: true,
			Detail: "local store not initialized; the hub-connected mark was not read"})
	}
	status, err := service.NewHubMarkService(app.store).Inspect(ctx, collectHubEvidence())
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark",
			Detail: fmt.Sprintf("cannot read the hub-connected mark: %v", err), Fix: "mtix sync doctor"})
	}
	if !status.Connected {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark", Pass: true,
			Detail: "hub-connected mark not set; no hub or relay evidence"})
	}
	return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark", Pass: true,
		Detail: "hub-connected mark set (source: " + status.Source + "); it is sticky and does not revert when config changes"})
}

// hubMarkConnected is the read-only answer of the hub mark: the stored mark,
// applied events above the reset floor, or local config and secrets evidence.
func hubMarkConnected() bool {
	if app.store == nil {
		return false
	}
	status, err := service.NewHubMarkService(app.store).Inspect(context.Background(), collectHubEvidence())
	return err == nil && status.Connected
}
