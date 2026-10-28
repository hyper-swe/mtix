// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/store"
)

// HistoryGuardWindow is how long a registration or daemon heartbeat of an
// older build keeps blocking history.export after it was last seen
// (MTIX-95.31.19.6). A machine that upgrades re-registers with the new version
// at once; one that never returns stops blocking after this window.
const HistoryGuardWindow = 30 * 24 * time.Hour

// HistoryGuardBlindSpot states what no signal can show, so the doctor row and
// the refusal never read as a proof that nothing else touches the board.
const HistoryGuardBlindSpot = "mtix cannot see other machines, CI jobs or clones that read .mtix/tasks.json through git: " +
	"mtix 0.5.4 records no session registration and the file names no writer while the switch is off; " +
	"run mtix --version on each of them and upgrade any older than " + FirstHistoryVersion + " before turning the switch on"

// GuardSignal is the service's own record of a build that has touched the
// board: a session registration or a daemon heartbeat. The service maps the
// store-layer record to it, so callers and tests never see a store type.
type GuardSignal struct {
	Source       string // registration or daemon
	Name         string
	Version      string
	Capabilities string // comma-separated markers the writer recorded
	SeenAt       string
}

// BinarySignalLister is the store capability the guard service needs.
type BinarySignalLister interface {
	BinarySignals(ctx context.Context) ([]store.BinarySignal, error)
}

// HistoryExportGuardService evaluates the history.export rollout guard for
// the doctor row and for config set (MTIX-95.31.19.6); handlers call it and
// never touch the store.
type HistoryExportGuardService struct {
	source BinarySignalLister
	clock  func() time.Time
}

// NewHistoryExportGuardService builds the guard service over a signal source
// and an injected clock.
func NewHistoryExportGuardService(source BinarySignalLister, clock func() time.Time) *HistoryExportGuardService {
	return &HistoryExportGuardService{source: source, clock: clock}
}

// Evaluate reads the signals and evaluates the guard at the injected clock's
// time. It returns an error when the signals cannot be read; callers treat
// that as blocked.
func (g *HistoryExportGuardService) Evaluate(ctx context.Context) (HistoryGuardReport, error) {
	if g == nil || g.source == nil {
		return HistoryGuardReport{}, errors.New("no board is open")
	}
	raw, err := g.source.BinarySignals(ctx)
	if err != nil {
		return HistoryGuardReport{}, fmt.Errorf("read binary signals: %w", err)
	}
	signals := make([]GuardSignal, 0, len(raw))
	for _, r := range raw {
		signals = append(signals, GuardSignal{Source: r.Source, Name: r.Name, Version: r.Version, Capabilities: r.Capabilities, SeenAt: r.SeenAt})
	}
	return EvaluateHistoryExportGuard(signals, g.clock()), nil
}

// HistoryGuardBlocker is one signal that a build without task history touches the board.
type HistoryGuardBlocker struct {
	Source  string `json:"source"` // registration or daemon
	Name    string `json:"name"`
	Version string `json:"version"`
	SeenAt  string `json:"seen_at,omitempty"`
}

// HistoryGuardReport is the outcome of the history.export rollout guard.
type HistoryGuardReport struct {
	Blockers   []HistoryGuardBlocker `json:"blockers"`
	MinVersion string                `json:"min_version"`
}

// Clear reports whether no older build was detected.
func (r HistoryGuardReport) Clear() bool { return len(r.Blockers) == 0 }

// EvaluateHistoryExportGuard decides whether history.export may be turned on
// (MTIX-95.31.19.6, ADR-006 A3.1). A registration or daemon heartbeat seen
// within HistoryGuardWindow (the edge included) that lacks the history reader
// marker (store.HistoryReaderCapability) was written by a build that keeps no
// task history and blocks. The version string is shown but never judged:
// released 0.5.4 prints dev and main builds stamp git-describe versions. A
// build that holds history writes the marker itself, so the current binary is
// never its own blocker. The last writer recorded in tasks.json is not judged:
// the file records none while the switch is off, and a 2.0.0 file proves
// nothing about who wrote it.
func EvaluateHistoryExportGuard(signals []GuardSignal, now time.Time) HistoryGuardReport {
	rep := HistoryGuardReport{MinVersion: FirstHistoryVersion}
	for _, s := range signals {
		if !seenWithinWindow(s.SeenAt, now) || hasCapability(s.Capabilities, store.HistoryReaderCapability) {
			continue
		}
		rep.Blockers = append(rep.Blockers, HistoryGuardBlocker{Source: s.Source, Name: s.Name, Version: s.Version, SeenAt: s.SeenAt})
	}
	return rep
}

// hasCapability reports whether the comma-separated list names capability.
func hasCapability(list, capability string) bool {
	for _, c := range strings.Split(list, ",") {
		if strings.TrimSpace(c) == capability {
			return true
		}
	}
	return false
}

// seenWithinWindow reports whether a signal was seen inside HistoryGuardWindow;
// a time that cannot be read counts as recent, the safe reading.
func seenWithinWindow(seen string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, seen)
	if err != nil {
		return true
	}
	return now.Sub(t) <= HistoryGuardWindow
}

// Refusal is the explained message printed when history.export is refused or
// the doctor row fails: what is detected, what a released reader does with the
// 2.1.0 file, the minimum version, and the exact fix.
func (r HistoryGuardReport) Refusal() string {
	var b strings.Builder
	fmt.Fprintf(&b, "history.export stays off: an mtix older than %s touches this board (", r.MinVersion)
	for i, bl := range r.Blockers {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s %s runs mtix %s without task history", bl.Source, bl.Name, displayVersion(bl.Version))
	}
	fmt.Fprintf(&b, "). With the switch on, tasks.json is export format 2.1.0 and mtix 0.5.4 and earlier "+
		"refuse it (import fails with checksum verification failed, automatic import loads nothing). "+
		"Fix: upgrade mtix to %s or later on every machine and daemon that touches the board, restart the daemon, "+
		"let each one write the board once, then run mtix doctor until the history.export.guard row passes and run mtix config set history.export true again",
		r.MinVersion)
	return b.String()
}

func displayVersion(v string) string {
	if v == "" {
		return "of an unrecorded version"
	}
	return v
}
