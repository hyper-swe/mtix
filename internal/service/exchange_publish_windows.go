// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

// Receipt publication remains unavailable pending native runtime verification.
package service

import "os"

func exchangePlatformSupported() bool       { return false }
func lockExchangeReader(_ *os.File) error   { return exchangeUnavailable("unsupported", nil) }
func publishExchangeFile(_, _ string) error { return exchangeUnavailable("unsupported", nil) }
