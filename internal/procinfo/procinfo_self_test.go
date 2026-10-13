// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo_test

import "os"

func procinfoSelf() int { return os.Getpid() }
