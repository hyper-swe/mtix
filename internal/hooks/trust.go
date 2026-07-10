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

// ConfigHash returns the SHA-256 (hex) of the raw hooks.yaml bytes at mtixDir,
// or "" when the file is absent. Trust binds to the bytes, not the path: any
// edit — local or arriving via sync — changes the hash and voids trust.
func ConfigHash(mtixDir string) string {
	data, err := os.ReadFile(filepath.Join(mtixDir, "hooks.yaml"))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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
