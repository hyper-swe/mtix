// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// TestMain isolates HOME, USERPROFILE, XDG_CONFIG_HOME, APPDATA and TMPDIR so
// command tests never read or write the developer's real user configuration
// (MTIX-112.39). Re-executed helper processes inherit the scratch root through
// hostguard.RootEnv and create nothing of their own.
func TestMain(m *testing.M) { os.Exit(hostguard.Run(m)) }
