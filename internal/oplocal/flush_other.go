// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin && !windows

package oplocal

import "os"

func platformFlush(f *os.File) error { return f.Sync() }
