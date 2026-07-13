// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Exec dispatch-host policy (MTIX-56.10): a HOST-LOCAL, never-synced knob
// controlling where exec hooks fire, complementing content-hash trust. Trust
// answers "may this config run code here at all"; the policy answers "which
// trigger on this host is allowed to run it":
//
//	any     every trigger dispatches (default — current behavior)
//	daemon  only a daemon-marked dispatcher runs dispatch passes on this
//	        host. CLI/server triggers no-op ENTIRELY — they claim nothing
//	        and advance nothing, so they can never consume a (hook,event)
//	        pair the daemon should fire, and every wake goes through the one
//	        supervised process
//	off     dispatch runs, but the exec adapter is skipped with the terminal
//	        outcome 'skipped-policy'; inbox/webhook/append-file deliver
//	        normally. For hosts that must never launch anything (a posting
//	        sandbox), regardless of trust state
//
// Like the trust hash, the mode lives in a local file beside hooks.yaml
// (gitignored, never synced) — placement decisions bind to a machine, so a
// synced or committed config can never carry its own dispatch policy.

// Exec dispatch modes.
const (
	ExecDispatchAny    = "any"
	ExecDispatchDaemon = "daemon"
	ExecDispatchOff    = "off"
)

// dispatchPolicyFileName is the local policy file beside hooks.yaml.
const dispatchPolicyFileName = "hooks.dispatch"

// ExecDispatchMode returns this host's exec dispatch mode (ExecDispatchAny
// when unset or unreadable — fail open to current behavior, never to a
// silently dead fabric).
func ExecDispatchMode(mtixDir string) string {
	data, err := os.ReadFile(filepath.Join(mtixDir, dispatchPolicyFileName))
	if err != nil {
		return ExecDispatchAny
	}
	switch mode := strings.TrimSpace(string(data)); mode {
	case ExecDispatchDaemon, ExecDispatchOff, ExecDispatchAny:
		return mode
	default:
		return ExecDispatchAny
	}
}

// SaveExecDispatchMode records the host-local exec dispatch mode.
func SaveExecDispatchMode(mtixDir, mode string) error {
	switch mode {
	case ExecDispatchAny, ExecDispatchDaemon, ExecDispatchOff:
	default:
		return fmt.Errorf("hooks: unknown exec-dispatch mode %q (want %s|%s|%s)",
			mode, ExecDispatchAny, ExecDispatchDaemon, ExecDispatchOff)
	}
	return os.WriteFile(filepath.Join(mtixDir, dispatchPolicyFileName), []byte(mode+"\n"), 0o600)
}
