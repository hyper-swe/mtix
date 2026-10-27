// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// validConfigKeys lists the 40 workspace keys per FR-11.2. Host wake keys
// are supplied separately by model.WakeSettingDefaults (FR-20.3).
//
// The sync.relay.* family configures the FR-21 file transport, which is
// EXPERIMENTAL and opt-in (trial use; behavior and on-disk format may
// change in 0.6.0; nothing changes unless sync.relay.dir is set). It is a
// second transport beside the hub, so its cadence and thresholds are
// its own settings rather than the hub's: the daemon's tick and a
// shared medium's propagation are unrelated concerns (D-R12).
var validConfigKeys = map[string]bool{
	"prefix":                       true,
	"author_id":                    true,
	"mcp.read_only":                true,
	"api.bind":                     true,
	"api.http_port":                true,
	"api.grpc_port":                true,
	"api.rate_limit":               true,
	"mcp.enabled":                  true,
	"mcp.transport":                true,
	"data.dir":                     true,
	"data.soft_delete_retention":   true,
	"history.export":               true,
	"history.max_entries_per_task": true,
	"sync.enabled":                 true,
	"sync.endpoint":                true,
	"sync.team_id":                 true,
	"sync.max_lamport_jump":        true,
	"sync.auto_sync":               true,
	"sync.interval":                true,
	"sync.keep_roles":              true,
	"sync.relay.dir":               true,
	"sync.relay.peer_id":           true,
	"sync.relay.poll_interval":     true,
	"sync.relay.retention_days":    true,
	"sync.relay.silent_peer_days":  true,
	"sync.relay.max_segment_bytes": true,
	"sync.relay.require_auth":      true,
	"agent.heartbeat_interval":     true,
	"agent.stale_threshold":        true,
	"agent.session_timeout":        true,
	"agent.stuck_timeout":          true,
	"agent.id_pattern":             true,
	"agent.auto_claim":             true,
	"context.token_estimator":      true,
	"logging.file":                 true,
	"logging.level":                true,
	"progress.weighted":            true,
	"ui.default_depth":             true,
	"ui.collapse_done":             true,
	"ui.theme":                     true,
}

// valueValidators check a value before it is stored, for keys where a
// malformed one fails somewhere far from the command that set it.
//
// The check belongs here so an operator learns at the moment they type
// it rather than at the next tick, when the message would arrive
// detached from the action that caused it.
var valueValidators = map[string]func(string) error{
	// FR-21 §5.2 identity grammar. Empty means "derive from this
	// machine", which is the default. The grammar is reused from the
	// relay's own frame layer rather than restated — a second copy of
	// an identity rule is a second thing to disagree.
	"sync.relay.peer_id": func(v string) error {
		if v == "" {
			return nil
		}
		if err := segment.ValidatePeerID(v); err != nil {
			return fmt.Errorf("sync.relay.peer_id %q is malformed "+
				"(16 hex characters, optionally -label): %w", v, model.ErrInvalidInput)
		}
		return nil
	},
	// MTIX-95.31.2: sync.auto_sync accepts true or false in any form;
	// a mistyped value is refused here rather than read as "on" later.
	"sync.auto_sync": func(v string) error {
		_, err := parseAutoSync(v)
		return err
	},
	"history.export":               validateHistoryExport,     // MTIX-95.31.19.5
	"history.max_entries_per_task": validateHistoryMaxEntries, // MTIX-95.31.19.4
}

// serverRestartKeys are keys that affect a running server and require restart.
var serverRestartKeys = map[string]bool{
	"api.bind":       true,
	"api.http_port":  true,
	"api.grpc_port":  true,
	"api.rate_limit": true,
	"mcp.enabled":    true,
	"mcp.transport":  true,
	"logging.file":   true,
	"logging.level":  true,
}

// configDefaults contains defaults for workspace keys per FR-11.2.
var configDefaults = map[string]string{
	"prefix":                       "PROJ",
	"author_id":                    "",
	"mcp.read_only":                "false",
	"api.bind":                     "127.0.0.1",
	"api.http_port":                "6849",
	"api.grpc_port":                "6850",
	"api.rate_limit":               "100",
	"mcp.enabled":                  "true",
	"mcp.transport":                "stdio",
	"data.dir":                     ".mtix/data",
	"data.soft_delete_retention":   "30d",
	"history.export":               "false",
	"history.max_entries_per_task": "",
	"sync.enabled":                 "false",
	"sync.endpoint":                "",
	"sync.team_id":                 "",
	"sync.max_lamport_jump":        maxLamportJumpDefault,
	"sync.auto_sync":               "true",
	"sync.interval":                "30s",
	"sync.keep_roles":              "",
	// EXPERIMENTAL, opt-in file relay. An empty relay directory means no
	// relay is configured; every relay phase is then a no-op rather than
	// an error.
	"sync.relay.dir": "",
	// An operator-assigned relay identity, for peers whose workspace is
	// rebuilt somewhere else between sessions. Empty derives it from
	// the machine, which is right whenever the machine is the thing
	// that persists.
	"sync.relay.peer_id": "",
	// Poll cadence is the relay's own, not the daemon's (D-R12). "0"
	// is legal and declares a tick-only peer (§6.6) — a turn-driven
	// agent seat, a cron-locked appliance, a sneakernet courier — which
	// is a deployment, not a misconfiguration.
	"sync.relay.poll_interval": "5s",
	// The prune safety window, and the silence threshold that prompts
	// retirement at twice it (§6.7).
	"sync.relay.retention_days":   "7",
	"sync.relay.silent_peer_days": "14",
	// Rotation threshold (§5.3).
	"sync.relay.max_segment_bytes": "4194304",
	// Authenticated by default: the relay medium is a less trusted
	// channel than the hub, so running without MACs is an explicit,
	// logged opt-out rather than something a peer drifts into (§8.3).
	"sync.relay.require_auth":  "true",
	"agent.heartbeat_interval": "60s",
	"agent.stale_threshold":    "24h",
	"agent.session_timeout":    "4h",
	"agent.stuck_timeout":      "",
	"agent.id_pattern":         "agent-*",
	"agent.auto_claim":         "true",
	"context.token_estimator":  "chars4",
	"logging.file":             ".mtix/logs/mtix.log",
	"logging.level":            "info",
	"progress.weighted":        "false",
	"ui.default_depth":         "3",
	"ui.collapse_done":         "false",
	"ui.theme":                 "system",
}
