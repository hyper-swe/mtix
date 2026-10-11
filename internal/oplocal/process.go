// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// PlacementLimit states the operator decision that filesystem checks cannot verify.
const PlacementLimit = "Filesystem checks cannot detect sandbox write access granted to an operator-owned directory. Keep operator state outside every sandbox-writable root."

// FromProcess reads the operator's process environment. Grant consumers must use
// the operator or daemon environment, never paths supplied by incoming content.
// Processes running as the operator share that operator's environment authority.
func FromProcess() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("resolve home: %w", err)
	}
	values := map[string]string{}
	for _, name := range []string{"XDG_CONFIG_HOME", "APPDATA", "TMPDIR", "CODEX_HOME"} {
		values[name] = os.Getenv(name)
	}
	if values["TMPDIR"] == "" {
		values["TMPDIR"] = os.TempDir()
	}
	return Env{GOOS: runtime.GOOS, Home: home, Values: values}, nil
}

func excludedRoots(env Env) []string {
	roots := []string{"/tmp", env.Values["TMPDIR"], env.Values["CODEX_HOME"]}
	if env.Home != "" {
		roots = append(roots, strings.TrimRight(env.Home, `\/`)+"/.codex")
	}
	return roots
}
