// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import "golang.org/x/sys/unix"

const terminalRequest = unix.TCGETS
