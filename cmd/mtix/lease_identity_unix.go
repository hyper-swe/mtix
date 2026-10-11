// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

// Unix wake ownership uses the effective process user.
package main

import (
	"os"
	"strconv"
)

func currentUserID() (string, error) { return strconv.Itoa(os.Geteuid()), nil }
