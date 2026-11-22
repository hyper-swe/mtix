// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"strconv"
	"strings"
)

// ProgressWeighted reports the progress.weighted switch (default off, FR-5.8):
// off, a parent's progress is count(done children) / count(included children)
// (FR-5.3); on, it is the weighted mean of its children's progress.
//
// It re-reads config.yaml on every call, like HistoryExport, so a long-running
// process (serve, the MCP server) follows `mtix config set progress.weighted`
// without a restart; the store asks once per write transaction. When the file
// cannot be read the value this process loaded stands. An unreadable value
// counts as off.
func (cs *ConfigService) ProgressWeighted() bool {
	raw, _ := cs.Get("progress.weighted")
	if cs.path != "" {
		if fresh, err := NewConfigService(cs.path); err == nil {
			raw, _ = fresh.Get("progress.weighted")
		}
	}
	on, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && on
}
