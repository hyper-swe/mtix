// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"strconv"
	"strings"
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
