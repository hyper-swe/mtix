// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
)

// The extension is shipped as an asset and checked statically: a live pi run is out of
// scope for MTIX-112.19.7 (the live leg is unproven).
func TestPiExtension_AssetContract(t *testing.T) {
	src := string(attach.PiExtensionSource())
	for _, want := range []string{
		"session_start", "before_agent_start", "session_shutdown", // lifecycle (V-P1, V-P2)
		"attach", "wait", "--harness", "pi", "--session", // the wait child
		"spawn(",                                     // argv spawn
		"triggerTurn: true", `deliverAs: "followUp"`, // V-P4 and V-L5
		"getSessionId()", // V-P2
		"MTIX_BIN",       // absolute path of mtix, never PATH lookup
	} {
		assert.Contains(t, src, want)
	}
	for _, bad := range []string{"exec(", "execSync", "shell: true", "child_process.exec", "eval(", "PRIME-TOKEN"} {
		assert.NotContains(t, src, bad, "the session id is argv only, never a shell")
	}
	assert.Contains(t, src, "path.isAbsolute", "the mtix binary must be an absolute path")
	assert.Equal(t, src, string(attach.PiExtensionSource()))
	b := attach.PiExtensionSource()
	if len(b) > 0 {
		b[0] ^= 0xff
		assert.NotEqual(t, b[0], attach.PiExtensionSource()[0], "callers get a copy")
	}
}

// Nothing a released binary does by default may write the extension (trunk rule 1).
func TestPiExtension_NoDefaultPathReferencesTheAsset(t *testing.T) {
	root := filepath.Join("..", "..")
	var hits []string
	for _, dir := range []string{"cmd", "internal"} {
		require.NoError(t, filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			if filepath.Base(p) == "pi_extension.go" {
				return nil
			}
			b, rerr := os.ReadFile(p) //nolint:gosec // G304: walks this repository's own source
			if rerr != nil {
				return rerr
			}
			if strings.Contains(string(b), "PiExtensionSource") {
				hits = append(hits, p)
			}
			return nil
		}))
	}
	assert.Empty(t, hits, "no production code may write or install the pi extension until the live pi gate passes")
}
