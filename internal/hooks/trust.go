// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// trustFileName names the legacy workspace record used only during migration.
const trustFileName = "hooks.trust"

// ConfigHash returns the legacy whole-config comparison digest (FR-19).
// Migration uses the corresponding captured input format; execution approval
// uses separate host-specific per-hook records.
func ConfigHash(mtixDir string) string {
	data, err := os.ReadFile(filepath.Join(mtixDir, "hooks.yaml"))
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write(data)

	// Fold in the content of each exec command element that resolves to a local
	// regular file (the wake-script), in config + command order for determinism.
	// Relative paths retain the legacy project-root comparison format;
	// non-file command elements are skipped.
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

// legacyRecordLimit bounds the legacy workspace record read (FR-19).
const legacyRecordLimit = 4096

// TrustedHash returns a legacy workspace comparison value for migration (FR-19).
// Only a small regular file is read; links, special files and oversize
// records yield an empty value.
func TrustedHash(mtixDir string) string {
	path := filepath.Join(mtixDir, trustFileName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > legacyRecordLimit {
		return ""
	}
	f, err := os.Open(path) //nolint:gosec // path is the fixed legacy record inside the operator's hooks directory
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, legacyRecordLimit+1))
	if err != nil || len(data) > legacyRecordLimit {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// ExecTrusted reports whether all configured exec hooks have current host approval (FR-19).
// It is a read-only aggregate; dispatch checks each hook independently.
func ExecTrusted(mtixDir string) bool {
	cfg, _ := Load(mtixDir)
	if !hasExec(cfg) {
		return false
	}
	approvals, err := ProcessApprovals(mtixDir)
	if err != nil {
		return false
	}
	decisions, err := approvals.Check()
	if err != nil || len(decisions) == 0 {
		return false
	}
	for _, decision := range decisions {
		if !decision.Trusted {
			return false
		}
	}
	return true
}
