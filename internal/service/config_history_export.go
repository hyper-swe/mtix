// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// HistoryExport reports the history.export switch (default off;
// MTIX-95.31.19.5, ADR-006 A3.1): whether tasks.json carries each task's
// change history in export format 2.1.0. EXPERIMENTAL until the doctor guard
// (MTIX-95.31.19.6) lands: a released mtix refuses a board that carries
// history.
//
// It re-reads config.yaml on every call, so a long-running process (the sync
// daemon, serve, the MCP server) follows `mtix config set history.export`
// without a restart; the store asks once per export. When the file cannot be
// read, or holds no usable value, the value this process loaded stands. An
// unreadable value counts as off.
func (cs *ConfigService) HistoryExport() bool {
	raw, _ := cs.Get("history.export")
	if cs.path != "" {
		if fresh, err := NewConfigService(cs.path); err == nil {
			raw, _ = fresh.Get("history.export")
		}
	}
	on, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && on
}

// validateHistoryExport accepts only true or false for history.export; a
// mistyped value is refused rather than read as off (MTIX-95.31.19.5).
func validateHistoryExport(v string) error {
	if _, err := strconv.ParseBool(strings.TrimSpace(v)); err != nil {
		return fmt.Errorf("history.export must be true or false, not %q: %w", v, model.ErrInvalidInput)
	}
	return nil
}

// SetHistoryExportGuard installs the rollout guard that Set consults before
// turning history.export on (MTIX-95.31.19.6). With none installed, as in
// library use, enabling is not guarded.
func (cs *ConfigService) SetHistoryExportGuard(check func() (HistoryGuardReport, error)) {
	cs.historyGuard = check
}

// guardHistoryExport refuses turning history.export on while the guard reports
// an older build touching the board, or cannot tell (fail closed); nothing is
// written then. Turning it off, other keys, and re-asserting a switch that is
// already on pass untouched (ADR-006 A3.1).
func (cs *ConfigService) guardHistoryExport(key, value string) error {
	if key != "history.export" || cs.historyGuard == nil {
		return nil
	}
	if on, err := strconv.ParseBool(strings.TrimSpace(value)); err != nil || !on || cs.HistoryExport() {
		return nil
	}
	rep, err := cs.historyGuard()
	if err != nil {
		return fmt.Errorf("history.export stays off: could not check whether an older mtix touches the board (%v); "+
			"run mtix doctor and fix what it reports: %w", err, model.ErrInvalidInput)
	}
	if rep.Clear() {
		return nil
	}
	return fmt.Errorf("%s: %w", rep.Refusal(), model.ErrInvalidInput)
}
