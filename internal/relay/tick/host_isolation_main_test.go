// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// TestMain isolates HOME, USERPROFILE, XDG_CONFIG_HOME, APPDATA and TMPDIR so
// no test in this package can read or write the developer's real user
// configuration (MTIX-112.39).
func TestMain(m *testing.M) { os.Exit(hostguard.Run(m)) }
