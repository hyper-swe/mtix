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
// this board, and mirrors a flip operator-local. It is best effort: a failure
// is reported on stderr and never fails the sync that already succeeded.
func noteHubConnection(ctx context.Context, stderr io.Writer) {
	if app.store == nil || app.mtixDir == "" {
		return
	}
	svc := service.NewHubMarkService(app.store)
	var status service.HubStatus
	var err error
	if os.Getenv(transport.EnvDSN) != "" {
		var flipped bool
		flipped, err = svc.RecordEnvConnection(ctx)
		status = service.HubStatus{Connected: true, Source: "env_dsn", Flipped: flipped}
	} else {
		status, err = svc.HubConnected(ctx, service.HubEvidence{SecretsDSN: true})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not record the hub-connected mark: %v\n", err)
		return
	}
	if status.Flipped {
		mirrorHubMark(stderr, status.Source)
	}
}

// mirrorHubMark writes the operator-local copy of the mark for this checkout.
func mirrorHubMark(stderr io.Writer, source string) {
	env, err := oplocal.FromProcess()
	if err == nil {
		err = oplocal.New(env).MirrorHubMark(filepath.Clean(filepath.Dir(app.mtixDir)), source)
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not mirror the hub-connected mark: %v\n", err)
	}
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
