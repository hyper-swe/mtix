// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// trustFileName is the LOCAL, per-operator record of the hooks.yaml that has
// been trusted to run exec hooks. It lives beside hooks.yaml but is gitignored
// and NEVER synced — trust binds to a specific operator's machine, so a synced
// or committed config can never carry its own approval (FR-19 §3 security).
const trustFileName = "hooks.trust"

// ConfigHash returns the SHA-256 (hex) of hooks.yaml AND the content of every
// local file an exec hook runs, at mtixDir; "" when hooks.yaml is absent. Trust
// binds to the bytes of BOTH the config and the code it invokes (MTIX-49): any
// edit — to hooks.yaml or to a referenced wake-script, local or arriving via
// sync — changes the hash and voids trust. This closes the approve-then-swap-
// the-payload escalation where the pin covered the config but not the script.
func ConfigHash(mtixDir string) string {
	data, err := os.ReadFile(filepath.Join(mtixDir, "hooks.yaml"))
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write(data)

	// Fold in the content of each exec command element that resolves to a local
	// regular file (the wake-script), in config + command order for determinism.
	// exec runs with cwd = the project root (the parent of .mtix), so relative
	// commands resolve there; PATH binaries / flags are not files and are skipped.
	cfg, _ := Load(mtixDir)
	projectRoot := filepath.Dir(mtixDir)
	for _, hook := range cfg.Hooks {
		if hook.Exec == nil {
			continue
		}
		for _, arg := range hook.Exec.Command {
			if content, ok := readTrustedScript(projectRoot, arg); ok {
				h.Write([]byte{0})
				h.Write([]byte(arg))
				h.Write([]byte{0})
				h.Write(content)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readTrustedScript reads a command element's content if it resolves to a local
// regular file (relative to root, or absolute); ok=false for PATH binaries,
// flags, or missing paths.
func readTrustedScript(root, arg string) ([]byte, bool) {
	p := arg
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, arg)
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	data, err := os.ReadFile(p) //nolint:gosec // hashing an operator-controlled hook script to pin its trust
	if err != nil {
		return nil, false
	}
	return data, true
}

// TrustedHash returns the hooks.yaml hash the local operator has trusted, or "".
func TrustedHash(mtixDir string) string {
	data, err := os.ReadFile(filepath.Join(mtixDir, trustFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveTrust records hash as the trusted hooks.yaml for this operator (local,
// gitignored, 0600).
func SaveTrust(mtixDir, hash string) error {
	return os.WriteFile(filepath.Join(mtixDir, trustFileName), []byte(hash+"\n"), 0o600)
}

// ExecTrusted reports whether the CURRENT hooks.yaml is trusted to run exec: it
// has content and its hash equals the operator's locally-trusted hash. The
// dispatcher calls this before firing any exec adapter; a mismatch (a fresh
// edit, or a teammate's change synced in) silently disables exec until the
// operator reviews and re-trusts, so an approve-then-edit escalation cannot ride
// a stale approval.
func ExecTrusted(mtixDir string) bool {
	cur := ConfigHash(mtixDir)
	return cur != "" && cur == TrustedHash(mtixDir)
}
