// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// progressWeightedCache holds the last progress.weighted value read from
// config.yaml and the file stamp (modification time and size) it was read at.
type progressWeightedCache struct {
	mu    sync.Mutex
	valid bool
	mod   time.Time
	size  int64
	on    bool
}

// ProgressWeighted reports the progress.weighted switch (default off, FR-5.8):
// off, a parent's progress is count(done children) / count(included children)
// (FR-5.3); on, it is the weighted mean of its children's progress.
//
// It follows config.yaml so a long-running process (serve, the MCP server)
// sees `mtix config set progress.weighted` without a restart, but it parses the
// file only when its modification time or size changed; the store asks once per
// write transaction, so the usual cost is one stat. When the file cannot be
// read the value this process loaded stands. A value that is not a boolean
// counts as off.
func (cs *ConfigService) ProgressWeighted() bool {
	if cs.path == "" {
		return parseSwitch(cs.values["progress.weighted"])
	}
	c := cs.progressWeighted
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Stat(cs.path)
	if err != nil {
		return parseSwitch(cs.values["progress.weighted"])
	}
	if c.valid && c.mod.Equal(info.ModTime()) && c.size == info.Size() {
		return c.on
	}
	raw := cs.values["progress.weighted"]
	if fresh, newErr := NewConfigService(cs.path); newErr == nil {
		raw = fresh.values["progress.weighted"]
	}
	c.valid, c.mod, c.size, c.on = true, info.ModTime(), info.Size(), parseSwitch(raw)
	return c.on
}

func parseSwitch(raw string) bool {
	on, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && on
}
