// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// collectHubEvidence gathers the local hub evidence, the one helper every
// replace guard shares (MTIX-107.31.8): the automatic import's evidence
// source, the command-line replace guard and the doctor check. It reports:
//   - ConfigKeys: a relay directory, or sync.enabled or sync.endpoint, in the
//     project config;
//   - SecretsDSN: a DSN in the secrets file, whatever its mode;
//   - DSNRefused: a DSN that transport.Source refuses to use (a DSN key in a
//     tracked config file, a secrets file with the wrong mode, even an empty
//     one): counted by the guards, transient, never the mark;
//   - EnvDSNExported: an exported MTIX_SYNC_DSN, which is only transient
//     evidence: it refuses a replace while it is set and is never recorded as
//     the sticky mark (ADR-007 §3.5). EnvDSNConnected stays false here: it is
//     set only after a command connects with the DSN.
//
// It fails closed: evidence that cannot be read (a secrets file that is not a
// regular file, a dangling link, a file that cannot be opened or is over the
// size cap, a config key that cannot be read) is an error, not "no evidence";
// a secrets file that does not exist is no evidence. The evidence read before
// the failure is returned beside the error, for callers that only describe the
// board and keep what they could read.
// The secrets content is only tested for presence, never read out.
func collectHubEvidence() (service.HubEvidence, error) {
	var ev service.HubEvidence
	dir, err := relayDirChecked()
	if err != nil {
		return ev, err
	}
	ev.ConfigKeys = dir != ""
	if app.configSvc != nil {
		for _, key := range []string{"sync.enabled", "sync.endpoint"} {
			v, getErr := app.configSvc.Get(key)
			if getErr != nil {
				return ev, fmt.Errorf("read %s: %w", key, getErr)
			}
			if v != "" && v != "false" {
				ev.ConfigKeys = true
			}
		}
	}
	if app.mtixDir == "" {
		return ev, nil
	}
	// Read before the secrets file, so evidence returned beside an error has it.
	ev.EnvDSNExported = os.Getenv(transport.EnvDSN) != ""
	body, _, secretsErr := transport.ReadSecretsFile(app.mtixDir)
	switch {
	case secretsErr == nil:
		ev.SecretsDSN = strings.TrimSpace(body) != ""
	case errors.Is(secretsErr, os.ErrNotExist):
		// ReadSecretsFile follows links: a dangling link reads as absent, but
		// something is there that cannot be read, so it is not "no evidence".
		path := filepath.Join(app.mtixDir, transport.SecretsFilename)
		if _, lstatErr := os.Lstat(path); lstatErr == nil {
			return ev, fmt.Errorf("the secrets file %s is a dangling link: %w", path, secretsErr)
		}
	default:
		return ev, fmt.Errorf("read the secrets file: %w", secretsErr)
	}
	// A DSN that Source refuses (a DSN key in a tracked config file, a secrets
	// file with the wrong mode, even an empty one) was always counted by the
	// replace guards, and is only transient evidence: the mark follows a DSN in
	// the secrets file by its content, as before.
	if _, srcErr := transport.Source(app.mtixDir); srcErr != nil && !errors.Is(srcErr, transport.ErrDSNNotConfigured) {
		ev.DSNRefused = true
	}
	return ev, nil
}

// lenientHubEvidence is collectHubEvidence for the permission diagnostics,
// which only describe the board and guard nothing: they keep the evidence
// read before a failure. The replace guards never use it.
func lenientHubEvidence() service.HubEvidence {
	ev, _ := collectHubEvidence() // the evidence read before a failure is kept
	return ev
}

// hubGuardCounts reports whether the replace guards count the local project as
// one that syncs beyond what the store records: the hub-connected mark, applied
// events, config, secrets or refused-DSN evidence, or an exported DSN. It fails
// closed: an error means the evidence could not be read, and the guard refuses
// (MTIX-107.31.8). The command-line replace guard and the doctor check call it.
func hubGuardCounts(ctx context.Context) (bool, error) {
	ev, err := collectHubEvidence()
	if err != nil {
		return true, fmt.Errorf("read the hub evidence: %w", err)
	}
	if ev.EnvDSNExported || ev.DSNRefused {
		return true, nil
	}
	if app.store == nil {
		return false, nil
	}
	status, err := service.NewHubMarkService(app.store).Inspect(ctx, ev)
	if err != nil {
		return true, fmt.Errorf("read the hub-connected mark: %w", err)
	}
	return status.Connected, nil
}

// noteHubConnection records that a sync or relay command reached a hub for
// this board, then brings the operator-local mirror up to date. It is best effort: a failure
// is reported on stderr and never fails the sync that already succeeded.
func noteHubConnection(ctx context.Context, stderr io.Writer) {
	if app.store == nil || app.mtixDir == "" {
		return
	}
	// The connect succeeded, so the DSN that was used counts as evidence now:
	// the environment one, or else the secrets file (transport.Source order).
	ev, evErr := collectHubEvidence()
	if evErr != nil {
		// The connect succeeded: unreadable evidence never lowers its record.
		// What was read before the failure is kept (a config key stays the source).
		fmt.Fprintf(stderr, "warning: could not read the hub evidence: %v\n", evErr)
	}
	ev.EnvDSNConnected = os.Getenv(transport.EnvDSN) != ""
	ev.SecretsDSN = ev.SecretsDSN || !ev.EnvDSNConnected
	_, err := service.NewHubMarkService(app.store).HubConnected(ctx, ev)
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
	mirrorErr := reconcileStoredHubMirror(state, checkout, mark.Set, mark.Source)
	var permissionErr error
	if mark.Set {
		permissionErr = observePermissionHubTransition()
	}
	return errors.Join(mirrorErr, permissionErr)
}

func observePermissionHubTransition() error {
	permissions, board, err := configuredPermissions()
	if err != nil {
		return fmt.Errorf("observe wake permission transition: %w", err)
	}
	if err = permissions.ObserveHub(context.Background(), board); err != nil {
		return fmt.Errorf("observe wake permission transition: %w", err)
	}
	return nil
}

func reconcileStoredHubMirror(state *oplocal.State, checkout string, marked bool, source string) error {
	mirror, _, err := state.HubMarkMirror(checkout)
	if err != nil {
		return fmt.Errorf("reconcile hub mark mirror: %w", err)
	}
	switch {
	case marked && mirror.Source != source:
		return state.MirrorHubMark(checkout, source)
	case !marked && mirror.Source != "":
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

// appendHubMarkCheck reports the sticky mark and its source (ADR-007 A9).
// Evidence it finds (config keys, a secrets DSN, applied events) is stored
// and mirrored, so what it reports as sticky is stored. An exported DSN
// that never connected is not evidence.
func appendHubMarkCheck(ctx context.Context, report DoctorReport) DoctorReport {
	return appendHubMarkCheckMode(ctx, report, true)
}

// Board mark persistence is shared; operator mirror and permission observation
// run only for the legacy collector and actual sync actions.
func appendHubMarkCheckMode(ctx context.Context, report DoctorReport, operatorWrites bool) DoctorReport {
	if app.store == nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark", Pass: true, Warn: true,
			Detail: "local store not initialized; the hub-connected mark was not read"})
	}
	evidence, evidenceErr := collectHubEvidence()
	if evidenceErr != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark",
			Detail: fmt.Sprintf("cannot read the hub evidence: %v", evidenceErr), Fix: "mtix sync doctor"})
	}
	status, err := service.NewHubMarkService(app.store).HubConnected(ctx, evidence)
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark",
			Detail: fmt.Sprintf("cannot read or store the hub-connected mark: %v", err), Fix: "mtix sync doctor"})
	}
	if !status.Connected {
		return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark", Pass: true,
			Detail: "hub-connected mark not set; no hub or relay evidence"})
	}
	detail := "hub-connected mark set (source: " + status.Source + "); it is stored and does not revert when config changes"
	if operatorWrites {
		if mirrorErr := reconcileHubMarkMirror(); mirrorErr != nil {
			detail += "; the operator-local copy could not be updated: " + mirrorErr.Error()
		}
	}
	return appendDoctorCheck(report, DoctorCheck{Name: "board.hub-mark", Pass: true, Detail: detail})
}

// hubMarkConnected is the read-only answer of the hub mark: the stored mark,
// applied events above the reset floor, or local config and secrets evidence.
func hubMarkConnected() bool {
	if app.store == nil {
		return false
	}
	evidence, _ := collectHubEvidence() // a stored mark, applied events and what was read still count
	status, err := service.NewHubMarkService(app.store).Inspect(context.Background(), evidence)
	return err == nil && status.Connected
}
