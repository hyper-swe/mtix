// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-49: exec trust pins hooks.yaml AND the content of every local script an
// exec hook runs. Editing a wake-script (approve-then-swap-the-payload) voids
// trust exactly like editing hooks.yaml — closing the escalation where the pin
// covered the config but not the code it invoked.

// setupExecHook builds a project root with .mtix/hooks.yaml (an exec hook that
// runs wake.sh) and wake.sh in the project root, returning (mtixDir, scriptPath).
func setupExecHook(t *testing.T, script string) (string, string) {
	t.Helper()
	root := t.TempDir()
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake
    match: { events: [comment.addressed], to-agent: worker }
    deliver: [exec]
    exec: { command: ["wake.sh"] }
`), 0o600))
	scriptPath := filepath.Join(root, "wake.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
	return mtixDir, scriptPath
}

func TestConfigHash_ChangesWhenExecScriptEdited(t *testing.T) {
	mtixDir, scriptPath := setupExecHook(t, "#!/bin/sh\necho original\n")
	h1 := ConfigHash(mtixDir)
	require.NotEmpty(t, h1)

	// Swap the payload behind an unchanged hooks.yaml.
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nevil\n"), 0o755))
	assert.NotEqual(t, h1, ConfigHash(mtixDir), "editing the exec script must change the trust hash")
}

func TestExecTrusted_VoidedByScriptEdit(t *testing.T) {
	mtixDir, scriptPath := setupExecHook(t, "#!/bin/sh\necho original\n")
	require.NoError(t, SaveTrust(mtixDir, ConfigHash(mtixDir)))
	require.True(t, ExecTrusted(mtixDir), "a freshly trusted config+script is trusted")

	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nevil\n"), 0o755))
	assert.False(t, ExecTrusted(mtixDir),
		"editing the script voids trust — exec is skipped until the operator re-trusts")
}

func TestConfigHash_StableWhenUnchanged(t *testing.T) {
	mtixDir, _ := setupExecHook(t, "#!/bin/sh\necho hi\n")
	assert.Equal(t, ConfigHash(mtixDir), ConfigHash(mtixDir), "deterministic over identical inputs")
}

func TestConfigHash_HooksYamlEditStillVoids(t *testing.T) {
	mtixDir, _ := setupExecHook(t, "#!/bin/sh\necho hi\n")
	h1 := ConfigHash(mtixDir)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake
    match: { events: [comment.addressed], to-agent: worker }
    deliver: [exec]
    exec: { command: ["wake.sh", "--now"] }
`), 0o600))
	assert.NotEqual(t, h1, ConfigHash(mtixDir), "editing hooks.yaml still voids trust (original pin intact)")
}

// TestConfigHash_MissingScript_NoPanic: a hook referencing an absent script must
// not panic; the missing file simply isn't folded in.
func TestConfigHash_MissingScript_NoPanic(t *testing.T) {
	root := t.TempDir()
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake
    match: { events: [status.changed] }
    deliver: [exec]
    exec: { command: ["does-not-exist.sh"] }
`), 0o600))
	assert.NotPanics(t, func() { _ = ConfigHash(mtixDir) })
	assert.NotEmpty(t, ConfigHash(mtixDir), "hooks.yaml still hashes even if a script is missing")
}
